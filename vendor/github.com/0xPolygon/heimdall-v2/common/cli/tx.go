package cli

import (
	"context"
	"fmt"

	"cosmossdk.io/log"
	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/tx"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/auth/ante"
	authsign "github.com/cosmos/cosmos-sdk/x/auth/signing"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/0xPolygon/heimdall-v2/bridge/util"
	"github.com/0xPolygon/heimdall-v2/helper"
)

func BroadcastMsg(clientCtx client.Context, sender string, msg sdk.Msg, logger log.Logger) error {
	// create tx factory
	txf, err := MakeTxFactory(clientCtx, sender, logger)
	if err != nil {
		logger.Error("Error creating tx factory", "Error", err)
		return err
	}
	// setting this to true to as the if block in BroadcastTx
	// might cause a canceled transaction.
	clientCtx.SkipConfirm = true
	account, err := util.GetAccount(context.Background(), clientCtx, sender)
	if err != nil {
		logger.Error("Error fetching account", "address", sender, "err", err)
		return err
	}
	clientCtx = clientCtx.WithFromAddress(account.GetAddress())
	from := clientCtx.GetFromAddress()
	authQueryClient := authtypes.NewQueryClient(clientCtx)
	_, err = authQueryClient.Account(context.Background(), &authtypes.QueryAccountRequest{Address: from.String()})
	if err != nil {
		logger.Error("Error fetching account", "Error", err)
		return err
	}

	_, err = txf.AccountRetriever().GetAccount(clientCtx, from)
	if err != nil {
		logger.Error("Error ensuring account exists", "Error", err)
		return err
	}

	txResponse, err := helper.BroadcastTx(clientCtx, txf, msg)
	if err != nil {
		logger.Error("Error broadcasting tx", "Error", err)
		return err
	}
	// Now check if the transaction response is not okay
	if txResponse.Code != abci.CodeTypeOK {
		logger.Error("Transaction response returned a non-ok code", "txResponseCode", txResponse.Code)
		return fmt.Errorf("broadcast succeeded but received non-ok response code: %d", txResponse.Code)
	}

	logger.Info(fmt.Sprintf("Tx with hash %s broadcasted successfully.", txResponse.TxHash))

	return nil
}

func MakeTxFactory(cliCtx client.Context, address string, logger log.Logger) (tx.Factory, error) {
	account, err := util.GetAccount(context.Background(), cliCtx, address)
	if err != nil {
		logger.Error("Error fetching account", "address", address, "err", err)
		return tx.Factory{}, err
	}

	accNum := account.GetAccountNumber()
	accSeq := account.GetSequence()

	signMode, err := authsign.APISignModeToInternal(cliCtx.TxConfig.SignModeHandler().DefaultMode())
	if err != nil {
		logger.Error("Error getting sign mode", "err", err)
		return tx.Factory{}, err
	}

	authParams, err := util.GetAccountParamsURL(cliCtx.Codec)
	if err != nil {
		logger.Error("Error getting account params", "err", err)
		return tx.Factory{}, err
	}

	chainParam, err := util.GetChainmanagerParams(cliCtx.Codec)
	if err != nil {
		return tx.Factory{}, err
	}

	txf := tx.Factory{}.
		WithTxConfig(cliCtx.TxConfig).
		WithAccountRetriever(cliCtx.AccountRetriever).
		WithChainID(chainParam.ChainParams.HeimdallChainId).
		WithSignMode(signMode).
		WithAccountNumber(accNum).
		WithSequence(accSeq).
		WithKeybase(cliCtx.Keyring).
		WithSignMode(signMode).
		WithFees(ante.DefaultFeeWantedPerTx.String()).
		WithGas(authParams.MaxTxGas)

	return txf, nil
}
