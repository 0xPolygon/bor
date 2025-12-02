package helper

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cosmos/cosmos-sdk/client"
	clienttx "github.com/cosmos/cosmos-sdk/client/tx"
	"github.com/cosmos/cosmos-sdk/codec/address"
	cosmossecp256k1 "github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
	"github.com/ethereum/go-ethereum/accounts/abi"
)

const (
	APIBodyLimit              = 128 * 1024 * 1024 // 128 MB
	mainnetChainID            = "heimdallv2-137"
	amoyChainID               = "heimdallv2-80002"
	mainnetInitialBlockHeight = 24404501
	amoyInitialBlockHeight    = 8788501
	devnetInitialBlockHeight  = 0
)

//go:generate mockgen -destination=./mocks/http_client_mock.go -package=mocks . HTTPClient
type HTTPClient interface {
	Get(string) (resp *http.Response, err error)
}

var Client HTTPClient

// GetFromAddress returns the from address from the context's name
func GetFromAddress(cliCtx client.Context) string {
	ac := address.NewHexCodec()
	fromAddress := cliCtx.GetFromAddress()
	if !fromAddress.Empty() {
		return fromAddress.String()
	}

	addr := GetAddress()
	addressString, err := ac.BytesToString(addr)
	if err != nil {
		panic(err)
	}
	return addressString
}

func init() {
	Client = &http.Client{
		Timeout: 10 * time.Second,
	}
}

// ToBytes32 is a convenience method for converting a byte slice to a fixed-sized 32-byte array.
// This method will truncate the input if it is larger than 32 bytes.
func ToBytes32(x []byte) [32]byte {
	var y [32]byte

	copy(y[:], x)

	return y
}

// GetPowerFromAmount returns power from amount -- note that this will populate the amount object
func GetPowerFromAmount(amount *big.Int) (*big.Int, error) {
	decimals18 := big.NewInt(0).Exp(big.NewInt(10), big.NewInt(18), nil)
	if amount.Cmp(decimals18) == -1 {
		return nil, errors.New("amount must be more than 1 token")
	}

	return amount.Div(amount, decimals18), nil
}

// UnpackSigAndVotes Unpacks Sig and Votes from Tx Payload
func UnpackSigAndVotes(payload []byte, abi abi.ABI) (votes []byte, sigs []byte, checkpointData []byte, err error) {
	// recover Method from signature and ABI
	method := abi.Methods["submitHeaderBlock"]
	decodedPayload := payload[4:]
	inputDataMap := make(map[string]interface{})
	// unpack method inputs
	err = method.Inputs.UnpackIntoMap(inputDataMap, decodedPayload)
	if err != nil {
		return
	}

	if inputDataMap["sigs"] == nil {
		inputDataMap["sigs"] = []byte{}
	}

	if inputDataMap["txData"] == nil {
		inputDataMap["txData"] = []byte{}
	}

	if inputDataMap["vote"] == nil {
		inputDataMap["vote"] = []byte{}
	}

	sigs = inputDataMap["sigs"].([]byte)
	checkpointData = inputDataMap["txData"].([]byte)
	votes = inputDataMap["vote"].([]byte)

	return
}

// EventByID looks up an event by the topic id
func EventByID(abiObject *abi.ABI, sigData []byte) *abi.Event {
	for _, event := range abiObject.Events {
		if bytes.Equal(event.ID.Bytes(), sigData) {
			return &event
		}
	}

	return nil
}

// GetHeimdallServerEndpoint returns heimdall server endpoint
func GetHeimdallServerEndpoint(endpoint string) string {
	url, found := strings.CutPrefix(conf.API.Address, "tcp")
	if !found {
		return url + endpoint
	}
	addr := "http" + url + endpoint
	return addr
}

// FetchFromAPI fetches data from any URL with a limited read size
func FetchFromAPI(URL string) ([]byte, error) {
	resp, err := Client.Get(URL)
	if err != nil {
		return nil, err
	}

	defer func() {
		if err = resp.Body.Close(); err != nil {
			Logger.Error("Error closing response body:", err)
		}
	}()

	// Limit the number of bytes read from the response body
	limitedBody := http.MaxBytesReader(nil, resp.Body, APIBodyLimit)

	// Handle the response

	if resp.StatusCode == 200 {
		body, err := io.ReadAll(limitedBody)
		if err != nil {
			return nil, err
		}

		return body, nil
	}

	Logger.Info("Error while fetching data from URL", "status", resp.StatusCode, "url", URL)

	return nil, fmt.Errorf("error while fetching data from url: %s, status: %d, error: %w", URL, resp.StatusCode, err)
}

// IsPubKeyFirstByteValid checks the validity of the public key's first byte.
// It must be 0x04 for uncompressed public keys
func IsPubKeyFirstByteValid(pubKey []byte) bool {
	prefix := make([]byte, 1)
	prefix[0] = byte(0x04)

	return bytes.Equal(prefix, pubKey[0:1])
}

// BroadcastTx attempts to generate, sign and broadcast a transaction with the
// given set of messages. It will also simulate gas requirements if necessary.
// It will return an error upon failure.
func BroadcastTx(clientCtx client.Context, txf clienttx.Factory, msgs ...sdk.Msg) (*sdk.TxResponse, error) {
	txf, err := txf.Prepare(clientCtx)
	if err != nil {
		return nil, err
	}

	if txf.SimulateAndExecute() || clientCtx.Simulate {
		if clientCtx.Offline {
			return nil, errors.New("cannot estimate gas in offline mode")
		}

		_, adjusted, err := clienttx.CalculateGas(clientCtx, txf, msgs...)
		if err != nil {
			return &sdk.TxResponse{
				Code: 1,
			}, err
		}

		txf = txf.WithGas(adjusted)
		_, _ = fmt.Fprintf(os.Stderr, "%s\n", clienttx.GasEstimateResponse{GasEstimate: txf.Gas()})
	}

	if clientCtx.Simulate {
		Logger.Debug("in simulate mode")

		return &sdk.TxResponse{
			Code: abci.CodeTypeOK,
		}, nil
	}

	tx, err := txf.BuildUnsignedTx(msgs...)
	if err != nil {
		Logger.Error("error while building unsigned tx", "error", err)
		return nil, err
	}

	if !clientCtx.SkipConfirm {
		panic("this should not happen as SkipConfirm is set to true")
	}

	cosmosPrivKey := &cosmossecp256k1.PrivKey{Key: GetPrivKey()}

	// First round: we gather all the signer infos. We use the "set empty
	// signature" hack to do that.
	var sigsV2 []signing.SignatureV2
	sigV2 := signing.SignatureV2{
		PubKey: cosmosPrivKey.PubKey(),
		Data: &signing.SingleSignatureData{
			SignMode:  txf.SignMode(),
			Signature: nil,
		},
		Sequence: txf.Sequence(),
	}

	sigsV2 = append(sigsV2, sigV2)
	err = tx.SetSignatures(sigsV2...)
	if err != nil {
		return nil, err
	}

	addrStr := sdk.MustHexifyAddressBytes(cosmosPrivKey.PubKey().Address())

	// Second round: all signer infos are set, so each signer can sign.
	sigsV2 = []signing.SignatureV2{}
	signerData := authsigning.SignerData{
		Address:       addrStr,
		ChainID:       txf.ChainID(),
		AccountNumber: txf.AccountNumber(),
		Sequence:      txf.Sequence(),
		PubKey:        cosmosPrivKey.PubKey(),
	}

	sigV2, err = clienttx.SignWithPrivKey(clientCtx.CmdContext, txf.SignMode(), signerData, tx, cosmosPrivKey, clientCtx.TxConfig, txf.Sequence())
	if err != nil {
		return nil, err
	}

	sigsV2 = append(sigsV2, sigV2)

	err = tx.SetSignatures(sigsV2...)
	if err != nil {
		return nil, err
	}

	txBytes, err := clientCtx.TxConfig.TxEncoder()(tx.GetTx())
	if err != nil {
		Logger.Error("error while encoding tx", "error", err)
		return nil, err
	}

	// broadcast to a CometBFT node
	res, err := clientCtx.BroadcastTx(txBytes)
	if err != nil {
		Logger.Error("error while broadcasting tx", "error", err)
		return nil, err
	}

	return res, nil
}

// SecureRandomInt generates a cryptographically secure random integer between minValue and maxLimit inclusive.
func SecureRandomInt(minValue, maxLimit int64) (int64, error) {
	if minValue > maxLimit {
		return 0, fmt.Errorf("invalid range: minValue cannot be greater than maxLimit")
	}
	if minValue == maxLimit {
		return minValue, nil
	}

	minBig := big.NewInt(minValue)
	maxBig := big.NewInt(maxLimit)

	// rangeSize = (maxLimit - minValue) + 1
	rangeSize := new(big.Int).Sub(maxBig, minBig)
	rangeSize.Add(rangeSize, big.NewInt(1))

	if rangeSize.Sign() <= 0 {
		return 0, fmt.Errorf("invalid range: non-positive range size")
	}

	// Generate a random number [0, rangeSize-1]
	nBig, err := rand.Int(rand.Reader, rangeSize)
	if err != nil {
		return 0, err
	}

	// Result = minValue + randomValue
	nBig.Add(nBig, minBig)

	return nBig.Int64(), nil
}

func GetInitialBlockHeight(chainID string) uint64 {
	switch chainID {
	case mainnetChainID:
		return mainnetInitialBlockHeight
	case amoyChainID:
		return amoyInitialBlockHeight
	default:
		return devnetInitialBlockHeight
	}
}
