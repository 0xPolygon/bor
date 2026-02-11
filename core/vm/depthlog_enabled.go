//go:build depthlog

package vm

import (
	"bufio"
	"fmt"
	"os"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"
)

const depthLogMaxBytes = 10 * 1024 * 1024 * 1024 // 10 GB

type depthLogger struct {
	mu           sync.Mutex
	f            *os.File
	w            *bufio.Writer
	bytesWritten int64
	stopped      bool
}

var (
	depthLogOnce     sync.Once
	depthLogInst     *depthLogger
	depthLogDiagOnce sync.Once
)

func initDepthLogger(evm *EVM) {
	if evm == nil {
		return
	}
	depthLogDiagOnce.Do(func() {
		log.Info("[depthlog] initDepthLogger first call", "DepthLogPath", evm.Config.DepthLogPath)
	})
	if evm.Config.DepthLogPath == "" {
		return
	}
	depthLogOnce.Do(func() {
		log.Info("Initializing depth logger", "path", evm.Config.DepthLogPath)
		file, err := os.OpenFile(evm.Config.DepthLogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			log.Error("Failed to open depth log file", "path", evm.Config.DepthLogPath, "err", err)
			return
		}
		var initialSize int64
		if info, serr := file.Stat(); serr == nil {
			initialSize = info.Size()
		}
		depthLogInst = &depthLogger{f: file, w: bufio.NewWriter(file), bytesWritten: initialSize}
		log.Info("Depth logger initialized successfully", "path", evm.Config.DepthLogPath, "existingBytes", initialSize)
	})
	if depthLogInst == nil {
		log.Info("Depth logger not available (file open failed?)", "path", evm.Config.DepthLogPath)
	}
	evm.depthLog = depthLogInst
}

func (l *depthLogger) logStorage(op string, addr common.Address, slot common.Hash, depth uint64, keyDepth uint64, valueBytes int, txHash common.Hash, txIndex int, blockHash common.Hash, blockNumber uint64) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped {
		return
	}
	if l.bytesWritten >= depthLogMaxBytes {
		l.stopped = true
		_ = l.w.Flush()
		log.Warn("Depth log file size limit reached, stopping logging", "limit", depthLogMaxBytes, "bytesWritten", l.bytesWritten)
		return
	}
	var n int
	switch {
	case txHash != (common.Hash{}) && txIndex >= 0 && blockHash != (common.Hash{}) && blockNumber > 0:
		n, _ = fmt.Fprintf(l.w, "{\"a\":\"%s\",\"k\":\"%s\",\"o\":\"%s\",\"d\":%d,\"p\":%d,\"b\":%d,\"th\":\"%s\",\"ti\":%d,\"bh\":\"%s\",\"bn\":%d}\n", addr.Hex(), slot.Hex(), op, depth, keyDepth, valueBytes, txHash.Hex(), txIndex, blockHash.Hex(), blockNumber)
	case txHash != (common.Hash{}) && txIndex >= 0:
		n, _ = fmt.Fprintf(l.w, "{\"a\":\"%s\",\"k\":\"%s\",\"o\":\"%s\",\"d\":%d,\"p\":%d,\"b\":%d,\"th\":\"%s\",\"ti\":%d}\n", addr.Hex(), slot.Hex(), op, depth, keyDepth, valueBytes, txHash.Hex(), txIndex)
	case txHash != (common.Hash{}):
		n, _ = fmt.Fprintf(l.w, "{\"a\":\"%s\",\"k\":\"%s\",\"o\":\"%s\",\"d\":%d,\"p\":%d,\"b\":%d,\"th\":\"%s\"}\n", addr.Hex(), slot.Hex(), op, depth, keyDepth, valueBytes, txHash.Hex())
	case txIndex >= 0:
		n, _ = fmt.Fprintf(l.w, "{\"a\":\"%s\",\"k\":\"%s\",\"o\":\"%s\",\"d\":%d,\"p\":%d,\"b\":%d,\"ti\":%d}\n", addr.Hex(), slot.Hex(), op, depth, keyDepth, valueBytes, txIndex)
	default:
		n, _ = fmt.Fprintf(l.w, "{\"a\":\"%s\",\"k\":\"%s\",\"o\":\"%s\",\"d\":%d,\"p\":%d,\"b\":%d}\n", addr.Hex(), slot.Hex(), op, depth, keyDepth, valueBytes)
	}
	l.bytesWritten += int64(n)
	_ = l.w.Flush()
}
