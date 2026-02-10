//go:build depthlog

package vm

import (
	"bufio"
	"fmt"
	"os"
	"sync"

	"github.com/ethereum/go-ethereum/common"
)

type depthLogger struct {
	mu sync.Mutex
	f  *os.File
	w  *bufio.Writer
}

var (
	depthLogOnce sync.Once
	depthLogInst *depthLogger
)

func initDepthLogger(evm *EVM) {
	if evm == nil {
		return
	}
	if evm.Config.DepthLogPath == "" {
		return
	}
	depthLogOnce.Do(func() {
		file, err := os.OpenFile(evm.Config.DepthLogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		depthLogInst = &depthLogger{f: file, w: bufio.NewWriter(file)}
	})
	evm.depthLog = depthLogInst
}

func (l *depthLogger) logStorage(op string, addr common.Address, slot common.Hash, depth uint64, keyDepth uint64, valueBytes int) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = fmt.Fprintf(l.w, "{\"a\":\"%s\",\"k\":\"%s\",\"o\":\"%s\",\"d\":%d,\"p\":%d,\"b\":%d}\n", addr.Hex(), slot.Hex(), op, depth, keyDepth, valueBytes)
	_ = l.w.Flush()
}
