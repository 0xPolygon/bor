package helper

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/0xPolygon/heimdall-v2/contracts/erc20"
	"github.com/0xPolygon/heimdall-v2/contracts/rootchain"
	"github.com/0xPolygon/heimdall-v2/contracts/stakemanager"
)

func GenerateAuthObj(client *ethclient.Client, address common.Address, data []byte) (auth *bind.TransactOpts, err error) {
	// generate call msg
	callMsg := ethereum.CallMsg{
		To:   &address,
		Data: data,
	}

	// get the private key
	pkObject := GetPrivKey()

	// create ecdsa private key
	ecdsaPrivateKey, err := crypto.ToECDSA(pkObject[:])
	if err != nil {
		return
	}

	// from address
	fromAddress := common.BytesToAddress(pkObject.PubKey().Address().Bytes())
	// fetch gasPrice
	gasPrice, err := client.SuggestGasPrice(context.Background())
	if err != nil {
		return
	}

	mainChainMaxGasPrice := GetConfig().MainChainMaxGasPrice
	// Check if configured or not, Use default in case of invalid value
	if mainChainMaxGasPrice <= 0 {
		mainChainMaxGasPrice = DefaultMainChainMaxGasPrice
	}

	if gasPrice.Cmp(big.NewInt(mainChainMaxGasPrice)) == 1 {
		Logger.Error("gas price is more than max gas price", "gasprice", gasPrice)
		err = fmt.Errorf("gas price is more than max_gas_price, gasprice = %v, maxGasPrice = %d", gasPrice, mainChainMaxGasPrice)

		return
	}

	nonce, err := client.NonceAt(context.Background(), fromAddress, nil)
	if err != nil {
		return
	}

	// fetch gas limit
	callMsg.From = fromAddress

	gasLimit, err := client.EstimateGas(context.Background(), callMsg)
	if err != nil {
		Logger.Error("unable to estimate gas", "error", err)
		return
	}

	chainId, err := client.ChainID(context.Background())
	if err != nil {
		Logger.Error("unable to fetch ChainID", "error", err)
		return
	}

	// create auth
	auth, err = bind.NewKeyedTransactorWithChainID(ecdsaPrivateKey, chainId)
	if err != nil {
		Logger.Error("unable to create auth object", "error", err)
		return
	}

	auth.GasPrice = gasPrice
	auth.Nonce = big.NewInt(int64(nonce))
	auth.GasLimit = gasLimit

	return
}

// SendCheckpoint sends checkpoint to rootChain contract
func (c *ContractCaller) SendCheckpoint(signedData []byte, sigs [][3]*big.Int, rootChainAddress common.Address, rootChainInstance *rootchain.Rootchain) error {
	data, err := c.RootChainABI.Pack("submitCheckpoint", signedData, sigs)
	if err != nil {
		Logger.Error("unable to pack tx for submitCheckpoint", "error", err)
		return err
	}

	auth, err := GenerateAuthObj(GetMainClient(), rootChainAddress, data)
	if err != nil {
		Logger.Error("unable to create auth object", "error", err)
		return err
	}

	s := make([]string, 0)
	for i := 0; i < len(sigs); i++ {
		s = append(s, fmt.Sprintf("[%s,%s,%s]", sigs[i][0].String(), sigs[i][1].String(), sigs[i][2].String()))
	}

	Logger.Debug("sending new checkpoint",
		"sigs", strings.Join(s, ","),
		"data", hex.EncodeToString(signedData),
	)

	tx, err := rootChainInstance.SubmitCheckpoint(auth, signedData, sigs)
	if err != nil {
		Logger.Error("error while submitting checkpoint", "error", err)
		return err
	}

	Logger.Info("submitted new checkpoint to rootChain successfully", "txHash", tx.Hash().String())

	return nil
}

// StakeFor stakes for a validator
func (c *ContractCaller) StakeFor(val common.Address, stakeAmount *big.Int, feeAmount *big.Int, acceptDelegation bool, stakeManagerAddress common.Address, stakeManagerInstance *stakemanager.Stakemanager) error {
	signerPubKey := GetPubKey()

	prefix := make([]byte, 1)
	prefix[0] = byte(0x04)

	if !bytes.Equal(prefix, signerPubKey[0:1]) {
		Logger.Error("public key first byte mismatch", "expected", "0x04", "received", signerPubKey[0:1])
		return errors.New("public key first byte mismatch")
	}
	// pack data based on method definition
	data, err := c.StakeManagerABI.Pack("stakeFor", val, stakeAmount, feeAmount, acceptDelegation, signerPubKey.Bytes())
	if err != nil {
		Logger.Error("unable to pack tx for stakeFor", "error", err)
		return err
	}

	auth, err := GenerateAuthObj(GetMainClient(), stakeManagerAddress, data)
	if err != nil {
		Logger.Error("unable to create auth object", "error", err)
		return err
	}

	// stake for stake manager
	tx, err := stakeManagerInstance.StakeFor(
		auth,
		val,
		stakeAmount,
		feeAmount,
		acceptDelegation,
		signerPubKey,
	)
	if err != nil {
		Logger.Error("error while submitting stake", "error", err)
		return err
	}

	Logger.Info("submitted stake successfully", "txHash", tx.Hash().String())

	return nil
}

// ApproveTokens approves pol token for stake
func (c *ContractCaller) ApproveTokens(amount *big.Int, stakeManager common.Address, tokenAddress common.Address, tokenInstance *erc20.Erc20) error {
	data, err := c.PolTokenABI.Pack("approve", stakeManager, amount)
	if err != nil {
		Logger.Error("unable to pack tx for approve", "error", err)
		return err
	}

	auth, err := GenerateAuthObj(GetMainClient(), tokenAddress, data)
	if err != nil {
		Logger.Error("unable to create auth object", "error", err)
		return err
	}

	tx, err := tokenInstance.Approve(auth, stakeManager, amount)
	if err != nil {
		Logger.Error("error while approving approve", "error", err)
		return err
	}

	Logger.Info("Sent approve tx successfully", "txHash", tx.Hash().String())

	return nil
}
