package util

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"cosmossdk.io/log"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	addressCodec "github.com/cosmos/cosmos-sdk/codec/address"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/0xPolygon/heimdall-v2/contracts/statesender"
	"github.com/0xPolygon/heimdall-v2/helper"
	chainmanagertypes "github.com/0xPolygon/heimdall-v2/x/chainmanager/types"
	checkpointTypes "github.com/0xPolygon/heimdall-v2/x/checkpoint/types"
	clerktypes "github.com/0xPolygon/heimdall-v2/x/clerk/types"
	milestoneTypes "github.com/0xPolygon/heimdall-v2/x/milestone/types"
	staketypes "github.com/0xPolygon/heimdall-v2/x/stake/types"
)

type BridgeEvent string

const (
	AccountDetailsURL       = "/cosmos/auth/v1beta1/accounts/%v"
	AccountParamsURL        = "/cosmos/auth/v1beta1/params"
	LastNoAckURL            = "/checkpoints/last-no-ack"
	CheckpointParamsURL     = "/checkpoints/params"
	CheckpointSignaturesURL = "/checkpoints/signatures/%v"
	ChainManagerParamsURL   = "/chainmanager/params"
	ProposersURL            = "/stake/proposers/%v"
	BufferedCheckpointURL   = "/checkpoints/buffer"
	LatestCheckpointURL     = "/checkpoints/latest"
	CountCheckpointURL      = "/checkpoints/count"
	CurrentProposerURL      = "/stake/proposers/current"
	LatestSpanURL           = "/bor/spans/latest"
	SpanByIdURL             = "/bor/spans/%v"
	NextSpanInfoURL         = "/bor/spans/prepare"
	NextSpanSeedURL         = "/bor/spans/seed/%v"
	ProducerVotesURL        = "/bor/producer-votes/%v"
	DividendAccountRootURL  = "/topup/dividend-account-root"
	ValidatorURL            = "/stake/validator/%v"
	CurrentValidatorSetURL  = "/stake/validators-set"
	StakingTxStatusURL      = "/stake/is-old-tx"
	TopupTxStatusURL        = "/topup/is-old-tx"
	ClerkTxStatusURL        = "/clerk/is-old-tx"
	ClerkEventRecordURL     = "/clerk/event-records/%d"
	LatestMilestoneURL      = "/milestones/latest"

	CometBFTUnconfirmedTxsURL      = "/unconfirmed_txs"
	CometBFTUnconfirmedTxsCountURL = "/num_unconfirmed_txs"

	TaskDelayBetweenEachVal = 10 * time.Second
	RetryTaskDelay          = 12 * time.Second
	RetryStateSyncTaskDelay = 24 * time.Second

	mempoolTxnCountDivisor = 1000

	StakingEvent BridgeEvent = "staking"
	TopupEvent   BridgeEvent = "topup"
	ClerkEvent   BridgeEvent = "clerk"

	BridgeDBFlag = "bridge-db"
)

// Logger returns logger singleton instance
func Logger() log.Logger {
	return helper.Logger.With("module", "bridge")
}

// IsProposer checks if the current is the proposer
func IsProposer(cdc codec.Codec) (bool, error) {
	logger := Logger()
	var (
		response staketypes.QueryProposersResponse
		count    = uint64(1)
	)

	result, err := helper.FetchFromAPI(helper.GetHeimdallServerEndpoint(fmt.Sprintf(ProposersURL, strconv.FormatUint(count, 10))))
	if err != nil {
		logger.Error("Error fetching proposers", "url", ProposersURL, "error", err)
		return false, err
	}

	if err := cdc.UnmarshalJSON(result, &response); err != nil {
		logger.Error("error unmarshalling proposer slice", "error", err)
		return false, err
	}

	ac := addressCodec.NewHexCodec()
	signerBytes, err := ac.StringToBytes(response.Proposers[0].Signer)
	if err != nil {
		logger.Error("Error converting signer string to bytes", "error", err)
		return false, err
	}
	if bytes.Equal(signerBytes, helper.GetAddress()) {
		return true, nil
	}

	return false, nil
}

// IsInProposerList checks if we are in the current proposers list
func IsInProposerList(count uint64, cdc codec.Codec) (bool, error) {
	logger := Logger()

	logger.Debug("Skipping proposers", "count", strconv.FormatUint(count+1, 10))

	response, err := helper.FetchFromAPI(helper.GetHeimdallServerEndpoint(fmt.Sprintf(ProposersURL, strconv.FormatUint(count+1, 10))))
	if err != nil {
		logger.Error("Unable to send request for next proposers", "url", ProposersURL, "error", err)
		return false, err
	}

	// unmarshall data from buffer
	var proposers staketypes.QueryProposersResponse
	if err := cdc.UnmarshalJSON(response, &proposers); err != nil {
		logger.Error("Error unmarshalling validator data ", "error", err)
		return false, err
	}

	logger.Debug("Fetched proposers list", "numberOfProposers", count+1)

	ac := addressCodec.NewHexCodec()

	for i := 1; i <= int(count) && i < len(proposers.Proposers); i++ {
		signerBytes, err := ac.StringToBytes(proposers.Proposers[i].Signer)
		if err != nil {
			logger.Error("Error converting signer string to bytes", "error", err)
			return false, err
		}
		if bytes.Equal(signerBytes, helper.GetAddress()) {
			return true, nil
		}
	}

	return false, nil
}

// CalculateTaskDelay calculates delay required for the current validator to propose the tx
// It solves for multiple validators sending the same transaction.
func CalculateTaskDelay(event interface{}, cdc codec.Codec) (bool, time.Duration) {
	logger := Logger()

	defer LogElapsedTimeForStateSyncedEvent(event, "CalculateTaskDelay", time.Now())

	// calculate validator position
	valPosition := 0
	isCurrentValidator := false

	validatorSet, err := GetValidatorSet(cdc)
	if err != nil {
		logger.Error("Error getting current validatorSet data ", "error", err)
		return false, 0
	}

	logger.Info("Fetched current validator set list", "currentValidatorCount", len(validatorSet.Validators))

	ac := addressCodec.NewHexCodec()
	for i, validator := range validatorSet.Validators {
		signerBytes, err := ac.StringToBytes(validator.Signer)
		if err != nil {
			logger.Error("Error converting signer string to bytes", "error", err)
			return false, 0
		}
		if bytes.Equal(signerBytes, helper.GetAddress()) {
			valPosition = i + 1
			isCurrentValidator = true

			break
		}
	}

	// Multiply the delay for every 1000 unconfirmed txs in the mempool.
	// For example, if the current default delay is 12 seconds,
	// then for up to 1000 txs it will stay as 12 only.
	// For 1000-2000 It will be 24 seconds.
	// For 2000-3000 it will be 36 seconds.
	// Basically, for every 1000 txs it will increase the factor by 1.

	mempoolFactor := GetUnconfirmedTxnCount(event) / mempoolTxnCountDivisor

	// calculate delay
	taskDelay := time.Duration(valPosition) * TaskDelayBetweenEachVal * time.Duration(mempoolFactor+1)

	return isCurrentValidator, taskDelay
}

// IsCurrentProposer checks if we are the current proposer
func IsCurrentProposer(cdc codec.Codec) (bool, error) {
	logger := Logger()

	var response staketypes.QueryCurrentProposerResponse

	result, err := helper.FetchFromAPI(helper.GetHeimdallServerEndpoint(CurrentProposerURL))
	if err != nil {
		logger.Error("Error fetching current proposer", "error", err)
		return false, err
	}

	if err = cdc.UnmarshalJSON(result, &response); err != nil {
		logger.Error("error unmarshalling current proposer response", "error", err)
		return false, err
	}

	logger.Debug("Current proposer fetched", "validator", response.Validator.String())

	ac := addressCodec.NewHexCodec()
	signerBytes, err := ac.StringToBytes(response.Validator.Signer)
	if err != nil {
		logger.Error("Error converting signer string to bytes", "error", err)
		return false, err
	}
	if bytes.Equal(signerBytes, helper.GetAddress()) {
		return true, nil
	}

	logger.Debug("We are not the current proposer")

	return false, nil
}

// IsEventSender checks if the validatorID belongs to the event sender
func IsEventSender(validatorID uint64, cdc codec.Codec) bool {
	logger := Logger()

	result, err := helper.FetchFromAPI(helper.GetHeimdallServerEndpoint(fmt.Sprintf(ValidatorURL, strconv.FormatUint(validatorID, 10))))
	if err != nil {
		logger.Error("Error fetching proposers", "error", err)
		return false
	}

	var validatorResponse staketypes.QueryValidatorResponse
	if err = cdc.UnmarshalJSON(result, &validatorResponse); err != nil {
		logger.Error("Error unmarshalling validator data", "error", err)
		return false
	}

	logger.Debug("Current event sender received", "validator", validatorResponse.Validator.String())

	ac := addressCodec.NewHexCodec()
	signerBytes, err := ac.StringToBytes(validatorResponse.Validator.Signer)
	if err != nil {
		logger.Error("Error converting signer string to bytes", "error", err)
		return false
	}
	return bytes.Equal(signerBytes, helper.GetAddress())
}

// CreateURLWithQuery receives the uri and parameters in key value form
// it will return the new url with the given query from the parameter
func CreateURLWithQuery(uri string, param map[string]interface{}) (string, error) {
	urlObj, err := url.Parse(uri)
	if err != nil {
		return uri, err
	}

	query := urlObj.Query()
	for k, v := range param {
		query.Set(k, fmt.Sprintf("%v", v))
	}

	urlObj.RawQuery = query.Encode()

	return urlObj.String(), nil
}

// IsCatchingUp checks if the heimdall node you are connected to is fully synced or not
// returns true when synced
func IsCatchingUp(cliCtx client.Context, ctx context.Context) bool {
	resp, err := helper.GetNodeStatus(cliCtx, ctx)
	if err != nil {
		return true
	}

	return resp.SyncInfo.CatchingUp
}

// GetAccount returns heimdall auth account
func GetAccount(ctx context.Context, cliCtx client.Context, address string) (sdk.AccountI, error) {
	var account sdk.AccountI
	cmt := helper.GetConfig().CometBFTRPCUrl
	rpc, err := client.NewClientFromNode(cmt)
	if err != nil {
		panic(err)
	}
	cliCtx = cliCtx.WithClient(rpc)

	queryClient := authtypes.NewQueryClient(cliCtx)

	// use the caller's ctx
	res, err := queryClient.Account(ctx, &authtypes.QueryAccountRequest{Address: address})
	if err != nil {
		return nil, err
	}

	if res == nil || res.Account == nil {
		return nil, fmt.Errorf("account not found for address %s", address)
	}

	if err := cliCtx.InterfaceRegistry.UnpackAny(res.Account, &account); err != nil {
		return nil, err
	}

	return account, nil
}

// GetAccountParamsURL return auth account params
func GetAccountParamsURL(cdc codec.Codec) (*authtypes.Params, error) {
	logger := Logger()

	response, err := helper.FetchFromAPI(helper.GetHeimdallServerEndpoint(AccountParamsURL))
	if err != nil {
		logger.Error("Error fetching account params", "err", err)
		return nil, err
	}

	var params authtypes.QueryParamsResponse
	if err = cdc.UnmarshalJSON(response, &params); err != nil {
		logger.Error("Error unmarshalling auth params", "url", AccountParamsURL, "err", err)
		return nil, err
	}

	return &params.Params, nil
}

// GetChainmanagerParams return chain manager params
func GetChainmanagerParams(cdc codec.Codec) (*chainmanagertypes.Params, error) {
	logger := Logger()

	response, err := helper.FetchFromAPI(helper.GetHeimdallServerEndpoint(ChainManagerParamsURL))
	if err != nil {
		logger.Error("Error fetching chainmanager params", "err", err)
		return nil, err
	}

	var params chainmanagertypes.QueryParamsResponse
	if err = cdc.UnmarshalJSON(response, &params); err != nil {
		logger.Error("Error unmarshalling chainmanager params", "url", ChainManagerParamsURL, "err", err)
		return nil, err
	}

	return &params.Params, nil
}

// GetCheckpointParams return checkpoint params
func GetCheckpointParams(cdc codec.Codec) (*checkpointTypes.Params, error) {
	logger := Logger()

	response, err := helper.FetchFromAPI(helper.GetHeimdallServerEndpoint(CheckpointParamsURL))
	if err != nil {
		logger.Error("Error fetching Checkpoint params", "err", err)
		return nil, err
	}

	var params checkpointTypes.QueryParamsResponse
	if err := cdc.UnmarshalJSON(response, &params); err != nil {
		logger.Error("Error unmarshalling Checkpoint params", "url", CheckpointParamsURL)
		return nil, err
	}

	return &params.Params, nil
}

// GetCheckpointAckCount return checkpoint ack count
func GetCheckpointAckCount(cdc codec.Codec) (uint64, error) {
	logger := Logger()

	response, err := helper.FetchFromAPI(helper.GetHeimdallServerEndpoint(CountCheckpointURL))
	if err != nil {
		logger.Error("Error fetching checkpoint ack count", "err", err)
		return 0, err
	}

	var ackCount checkpointTypes.QueryAckCountResponse
	if err := cdc.UnmarshalJSON(response, &ackCount); err != nil {
		logger.Error("Error unmarshalling checkpoint ack count", "url", CountCheckpointURL, "err", err)
		return 0, err
	}

	return ackCount.AckCount, nil
}

// GetBufferedCheckpoint return checkpoint from buffer
func GetBufferedCheckpoint(cdc codec.Codec) (*checkpointTypes.Checkpoint, error) {
	logger := Logger()

	response, err := helper.FetchFromAPI(helper.GetHeimdallServerEndpoint(BufferedCheckpointURL))
	if err != nil {
		logger.Debug("Error fetching buffered checkpoint", "err", err)
		return nil, err
	}

	var checkpoint checkpointTypes.QueryCheckpointBufferResponse
	if err := cdc.UnmarshalJSON(response, &checkpoint); err != nil {
		logger.Error("Error unmarshalling buffered checkpoint", "url", BufferedCheckpointURL, "err", err)
		return nil, err
	}

	return &checkpoint.Checkpoint, nil
}

// GetLatestCheckpoint return last successful checkpoint
func GetLatestCheckpoint(cdc codec.Codec) (*checkpointTypes.Checkpoint, error) {
	logger := Logger()

	response, err := helper.FetchFromAPI(helper.GetHeimdallServerEndpoint(LatestCheckpointURL))
	if err != nil {
		logger.Debug("Error fetching latest checkpoint", "err", err)
		return nil, err
	}

	var checkpoint checkpointTypes.QueryCheckpointLatestResponse
	if err = cdc.UnmarshalJSON(response, &checkpoint); err != nil {
		logger.Error("Error unmarshalling latest checkpoint", "url", LatestCheckpointURL, "err", err)
		return nil, err
	}

	return &checkpoint.Checkpoint, nil
}

// AppendPrefix returns PublicKey in uncompressed format
func AppendPrefix(signerPubKey []byte) []byte {
	// Append the prefix "0x04", because heimdall uses publicKey in an uncompressed format.
	// See https://superuser.com/questions/1465455/what-is-the-size-of-public-key-for-ecdsa-spec256r1
	prefix := make([]byte, 1)
	prefix[0] = byte(0x04)
	signerPubKey = append(prefix[:], signerPubKey[:]...)

	return signerPubKey
}

// GetValidatorNonce fetches validator nonce and height
func GetValidatorNonce(validatorID uint64, cdc codec.Codec) (uint64, error) {
	logger := Logger()

	result, err := helper.FetchFromAPI(helper.GetHeimdallServerEndpoint(fmt.Sprintf(ValidatorURL, strconv.FormatUint(validatorID, 10))))
	if err != nil {
		logger.Error("Error fetching validator data", "error", err)
		return 0, err
	}

	var validatorResponse staketypes.QueryValidatorResponse
	if err = cdc.UnmarshalJSON(result, &validatorResponse); err != nil {
		logger.Error("Error unmarshalling validator data ", "error", err)
		return 0, err
	}

	logger.Debug("Validator data received ", "validator", validatorResponse.Validator.String())

	return validatorResponse.Validator.Nonce, nil
}

// GetValidatorSet fetches the current validator set
func GetValidatorSet(cdc codec.Codec) (*staketypes.ValidatorSet, error) {
	logger := Logger()

	response, err := helper.FetchFromAPI(helper.GetHeimdallServerEndpoint(CurrentValidatorSetURL))
	if err != nil {
		logger.Error("Unable to send request for current validatorSet", "url", CurrentValidatorSetURL, "error", err)
		return nil, err
	}

	var validatorSetResponse staketypes.QueryCurrentValidatorSetResponse
	if err = cdc.UnmarshalJSON(response, &validatorSetResponse); err != nil {
		logger.Error("Error unmarshalling validator set data ", "error", err)
		return nil, err
	}

	return &validatorSetResponse.ValidatorSet, nil
}

// GetClerkEventRecord return last successful checkpoint
func GetClerkEventRecord(stateId int64, cdc codec.Codec) (*clerktypes.EventRecord, error) {
	logger := Logger()

	response, err := helper.FetchFromAPI(helper.GetHeimdallServerEndpoint(fmt.Sprintf(ClerkEventRecordURL, stateId)))
	if err != nil {
		logger.Error("Error fetching event record by state ID", "error", err)
		return nil, err
	}

	var eventRecordResponse clerktypes.RecordResponse
	if err = cdc.UnmarshalJSON(response, &eventRecordResponse); err != nil {
		logger.Error("Error unmarshalling event record", "error", err)
		return nil, err
	}

	return &eventRecordResponse.Record, nil
}

func GetUnconfirmedTxnCount(event interface{}) int {
	logger := Logger()

	defer LogElapsedTimeForStateSyncedEvent(event, "GetUnconfirmedTxnCount", time.Now())

	endpoint := helper.GetConfig().CometBFTRPCUrl + CometBFTUnconfirmedTxsCountURL

	resp, err := helper.Client.Get(endpoint)
	if err != nil || resp.StatusCode != http.StatusOK {
		logger.Error("Error fetching mempool txs count", "url", endpoint, "error", err)
		return 0
	}

	// Limit the number of bytes read from the response body
	limitedBody := http.MaxBytesReader(nil, resp.Body, helper.APIBodyLimit)

	body, err := io.ReadAll(limitedBody)
	defer func() {
		if err := resp.Body.Close(); err != nil {
			logger.Error("Error closing response body:", err)
		}
	}()

	if err != nil {
		logger.Error("Error fetching mempool txs count", "error", err)
		return 0
	}

	// a minimal response of the unconfirmed txs
	var response CometBFTUnconfirmedTxs

	err = json.Unmarshal(body, &response)
	if err != nil {
		logger.Error("Error unmarshalling response received from Heimdall Server", "error", err)
		return 0
	}

	count, _ := strconv.Atoi(response.Result.Total)

	return count
}

// LogElapsedTimeForStateSyncedEvent logs useful info for StateSynced events
func LogElapsedTimeForStateSyncedEvent(event interface{}, functionName string, startTime time.Time) {
	logger := Logger()

	if event == nil {
		return
	}

	var (
		typedEvent  statesender.StatesenderStateSynced
		timeElapsed = time.Since(startTime).Milliseconds()
	)

	switch e := event.(type) {
	case statesender.StatesenderStateSynced:
		typedEvent = e
	case *statesender.StatesenderStateSynced:
		if e == nil {
			return
		}

		typedEvent = *e
	default:
		return
	}

	logger.Info("StateSyncedEvent: "+functionName,
		"stateSyncId", typedEvent.Id,
		"timeElapsed", timeElapsed)
}

// GetLatestMilestone return last successful milestone
func GetLatestMilestone(cdc codec.Codec) (*milestoneTypes.Milestone, error) {
	logger := Logger()

	response, err := helper.FetchFromAPI(helper.GetHeimdallServerEndpoint(LatestMilestoneURL))
	if err != nil {
		logger.Debug("Error fetching latest milestone", "err", err)
		return nil, err
	}

	var milestone milestoneTypes.QueryLatestMilestoneResponse
	if err = cdc.UnmarshalJSON(response, &milestone); err != nil {
		logger.Error("Error unmarshalling latest milestone", "url", LatestMilestoneURL, "err", err)
		return nil, err
	}

	return &milestone.Milestone, nil
}
