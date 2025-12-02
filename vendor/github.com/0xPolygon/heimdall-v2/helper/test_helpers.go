package helper

import (
	abci "github.com/cometbft/cometbft/abci/types"
)

type TestOpts struct {
	app     abci.Application
	chainId string
}

func (t *TestOpts) SetApplication(app abci.Application) {
	t.app = app
}

func (t *TestOpts) GetApplication() abci.Application {
	return t.app
}

func (t *TestOpts) SetChainId(chainId string) {
	t.chainId = chainId
}

func (t *TestOpts) GetChainId() string {
	return t.chainId
}
