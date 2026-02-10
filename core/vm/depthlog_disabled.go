//go:build !depthlog

package vm

import "github.com/ethereum/go-ethereum/common"

type depthLogger struct{}

func initDepthLogger(_ *EVM) {}

func (l *depthLogger) logStorage(_ string, _ common.Address, _ common.Hash, _ uint64, _ uint64, _ int) {
}
