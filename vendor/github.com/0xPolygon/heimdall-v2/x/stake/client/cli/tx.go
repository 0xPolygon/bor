package cli

import (
	"bytes"
	"fmt"

	"cosmossdk.io/core/address"
	sdkmath "cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/client"
	codec "github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/ethereum/go-ethereum/common"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/0xPolygon/heimdall-v2/common/cli"
	"github.com/0xPolygon/heimdall-v2/contracts/stakinginfo"
	"github.com/0xPolygon/heimdall-v2/helper"
	chainmanagerTypes "github.com/0xPolygon/heimdall-v2/x/chainmanager/types"
	"github.com/0xPolygon/heimdall-v2/x/stake/types"
)

var logger = helper.Logger.With("module", "stake/client/cli")

// NewTxCmd returns a root CLI command handler for all x/stake transaction commands.
func NewTxCmd() *cobra.Command {
	txCmd := &cobra.Command{
		Use:                        types.ModuleName,
		Short:                      "Stake transaction subcommands",
		DisableFlagParsing:         true,
		SuggestionsMinimumDistance: 2,
		RunE:                       client.ValidateCmd,
	}

	ac := codec.NewHexCodec()

	txCmd.AddCommand(
		NewValidatorJoinCmd(ac),
		NewSignerUpdateCmd(ac),
	)

	return txCmd
}

// NewValidatorJoinCmd returns a CLI command handler for creating a MsgValidatorJoin transaction.
func NewValidatorJoinCmd(ac address.Codec) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validator-join",
		Short: "send validator join tx",
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientTxContext(cmd)
			if err != nil {
				return err
			}

			// get proposer
			proposer := viper.GetString(FlagProposerAddress)
			if proposer == "" {
				proposer = clientCtx.GetFromAddress().String()
			}

			_, err = ac.StringToBytes(proposer)
			if err != nil {
				return fmt.Errorf("the proposer address is invalid: %w", err)
			}

			txHash := viper.GetString(FlagTxHash)
			if txHash == "" {
				return fmt.Errorf("the provided transaction hash is empty, and the field is required")
			}

			pubKeyStr := viper.GetString(FlagSignerPubKey)
			if pubKeyStr == "" {
				return fmt.Errorf("the provided PubKey is empty, and the field is required")
			}

			pubKeyBytes := common.FromHex(pubKeyStr)
			if len(pubKeyBytes) != 65 {
				return fmt.Errorf("the provided PubKey length is invalid")
			}
			pubKey := secp256k1.PubKey{
				Key: pubKeyBytes,
			}

			// total stake amount
			amount, ok := sdkmath.NewIntFromString(viper.GetString(FlagAmount))
			if !ok {
				return fmt.Errorf("invalid stake amount provided")
			}

			contractCaller, err := helper.NewContractCaller()
			if err != nil {
				return err
			}

			// fetch params
			queryClient := chainmanagerTypes.NewQueryClient(clientCtx)
			cmParams, err := queryClient.GetChainManagerParams(cmd.Context(), &chainmanagerTypes.QueryParamsRequest{})
			if err != nil {
				return err
			}

			// get main tx receipt
			receipt, err := contractCaller.GetConfirmedTxReceipt(common.HexToHash(txHash), cmParams.Params.MainChainTxConfirmations)
			if err != nil || receipt == nil {
				return fmt.Errorf("transaction %s is not confirmed yet, please wait for some time and try again", txHash)
			}

			abiObject := &contractCaller.StakingInfoABI
			eventName := "Staked"
			event := new(stakinginfo.StakinginfoStaked)
			var logIndex uint64
			found := false
			for _, vLog := range receipt.Logs {
				topic := vLog.Topics[0].Bytes()
				selectedEvent := helper.EventByID(abiObject, topic)
				if selectedEvent != nil && selectedEvent.Name == eventName {
					if err = helper.UnpackLog(abiObject, event, eventName, vLog); err != nil {
						return err
					}
					logIndex = uint64(vLog.Index)
					found = true
					break
				}
			}

			if !found {
				return fmt.Errorf("invalid tx %s for validator join", txHash)
			}

			if !helper.IsPubKeyFirstByteValid(pubKey.Bytes()[0:1]) {
				return fmt.Errorf("public key first byte mismatch")
			}

			if !bytes.Equal(event.SignerPubkey, pubKey.Bytes()[1:]) {
				return fmt.Errorf("public key mismatch with event log")
			}

			msg, err := types.NewMsgValidatorJoin(proposer, event.ValidatorId.Uint64(), event.ActivationEpoch.Uint64(), amount, &pubKey, common.FromHex(txHash), logIndex, receipt.BlockNumber.Uint64(), event.Nonce.Uint64())
			if err != nil {
				return err
			}

			return cli.BroadcastMsg(clientCtx, proposer, msg, logger)
		},
	}

	cmd.Flags().StringP(FlagProposerAddress, "p", "", "--proposer=<proposer-address>")
	cmd.Flags().String(FlagSignerPubKey, "", "--signer-pubkey=<signer-pubkey-here>")
	cmd.Flags().String(FlagTxHash, "", "--tx-hash=<transaction-hash>")
	cmd.Flags().Uint64(FlagBlockNumber, 0, "--block-number=<block-number>")
	cmd.Flags().String(FlagAmount, "0", "--staked-amount=<amount>")
	cmd.Flags().Uint64(FlagActivationEpoch, 0, "--activation-epoch=<activation-epoch>")

	if err := cmd.MarkFlagRequired(FlagBlockNumber); err != nil {
		logger.Error("SendValidatorJoinTx | MarkFlagRequired | FlagBlockNumber", "Error", err)
	}

	if err := cmd.MarkFlagRequired(FlagActivationEpoch); err != nil {
		logger.Error("SendValidatorJoinTx | MarkFlagRequired | FlagActivationEpoch", "Error", err)
	}

	if err := cmd.MarkFlagRequired(FlagAmount); err != nil {
		logger.Error("SendValidatorJoinTx | MarkFlagRequired | FlagAmount", "Error", err)
	}

	if err := cmd.MarkFlagRequired(FlagSignerPubKey); err != nil {
		logger.Error("SendValidatorJoinTx | MarkFlagRequired | FlagSignerPubKey", "Error", err)
	}

	if err := cmd.MarkFlagRequired(FlagTxHash); err != nil {
		logger.Error("SendValidatorJoinTx | MarkFlagRequired | FlagTxHash", "Error", err)
	}

	return cmd
}

// NewSignerUpdateCmd returns a CLI command handler for creating a MsgSignerUpdate transaction.
func NewSignerUpdateCmd(ac address.Codec) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "signer-update",
		Short: "Update signer for a validator",
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientTxContext(cmd)
			if err != nil {
				return err
			}

			// get proposer
			proposer := viper.GetString(FlagProposerAddress)
			if proposer == "" {
				proposer = clientCtx.GetFromAddress().String()
			}

			valId := viper.GetUint64(FlagValidatorID)
			if valId == 0 {
				return fmt.Errorf("validator id cannot be 0")
			}

			_, err = ac.StringToBytes(proposer)
			if err != nil {
				return fmt.Errorf("the proposer address is invalid: %w", err)
			}

			txHash := viper.GetString(FlagTxHash)
			if txHash == "" {
				return fmt.Errorf("the provided transaction hash is empty, and the field is required")
			}

			pubKeyStr := viper.GetString(FlagNewSignerPubKey)
			if pubKeyStr == "" {
				return fmt.Errorf("the provided PubKey is empty, and the field is required")
			}

			pubKeyBytes := common.FromHex(pubKeyStr)
			if len(pubKeyBytes) != 65 {
				return fmt.Errorf("the provided PubKey length is invalid")
			}
			pubKey := secp256k1.PubKey{
				Key: pubKeyBytes,
			}

			if !helper.IsPubKeyFirstByteValid(pubKey.Bytes()[0:1]) {
				return fmt.Errorf("public key first byte mismatch")
			}

			msg, err := types.NewMsgSignerUpdate(proposer, valId, pubKey.Bytes(), common.FromHex(txHash), viper.GetUint64(FlagLogIndex), viper.GetUint64(FlagBlockNumber), viper.GetUint64(FlagNonce))
			if err != nil {
				return err
			}

			return cli.BroadcastMsg(clientCtx, proposer, msg, logger)
		},
	}

	cmd.Flags().StringP(FlagProposerAddress, "p", "", "--proposer=<proposer-address>")
	cmd.Flags().Uint64(FlagValidatorID, 0, "--id=<validator-id>")
	cmd.Flags().String(FlagNewSignerPubKey, "", "--new-pubkey=<new-signer-pubkey>")
	cmd.Flags().String(FlagTxHash, "", "--tx-hash=<transaction-hash>")
	cmd.Flags().Uint64(FlagLogIndex, 0, "--log-index=<log-index>")
	cmd.Flags().Uint64(FlagBlockNumber, 0, "--block-number=<block-number>")
	cmd.Flags().Int(FlagNonce, 0, "--nonce=<nonce>")

	if err := cmd.MarkFlagRequired(FlagValidatorID); err != nil {
		logger.Error("SendValidatorUpdateTx | MarkFlagRequired | FlagValidatorID", "Error", err)
	}

	if err := cmd.MarkFlagRequired(FlagTxHash); err != nil {
		logger.Error("SendValidatorUpdateTx | MarkFlagRequired | FlagTxHash", "Error", err)
	}

	if err := cmd.MarkFlagRequired(FlagNewSignerPubKey); err != nil {
		logger.Error("SendValidatorUpdateTx | MarkFlagRequired | FlagNewSignerPubKey", "Error", err)
	}

	if err := cmd.MarkFlagRequired(FlagLogIndex); err != nil {
		logger.Error("SendValidatorUpdateTx | MarkFlagRequired | FlagLogIndex", "Error", err)
	}

	if err := cmd.MarkFlagRequired(FlagBlockNumber); err != nil {
		logger.Error("SendValidatorUpdateTx | MarkFlagRequired | FlagBlockNumber", "Error", err)
	}

	if err := cmd.MarkFlagRequired(FlagNonce); err != nil {
		logger.Error("SendValidatorUpdateTx | MarkFlagRequired | FlagNonce", "Error", err)
	}

	return cmd
}
