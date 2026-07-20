// Copyright 2024 The go-ethereum Authors
// This file is part of the go-ethereum library.

package tracers

import (
	"encoding/json"

	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/params"
)

// parityCallTracerName is the registered tracer backing the Parity trace
// conversion. It wraps the native callTracer and additionally captures the gas
// remaining at the end of EVM execution (before refund/floor/leftover), so the
// root trace's gasUsed can report the gross execution gas matching erigon.
const parityCallTracerName = "parityCallTracer"

func init() {
	DefaultDirectory.Register(parityCallTracerName, newParityCallTracer, false)
}

// parityCallResult is the JSON returned by the wrapper: the underlying
// callTracer frame plus the gas remaining after execution.
type parityCallResult struct {
	Frame      json.RawMessage `json:"frame"`
	GasLeft    uint64          `json:"gasLeft"`
	GasLeftSet bool            `json:"gasLeftSet"`
}

// parityCallTracer wraps the native callTracer and records the gas remaining at
// the moment execution finishes (captured as the "old" value of the first
// transaction-finalization OnGasChange event: refunds, data floor, or leftover
// return). gasLimit - gasLeft - intrinsic then yields the gross execution gas.
type parityCallTracer struct {
	inner      *Tracer
	gasLeft    uint64
	gasLeftSet bool
}

// newParityCallTracer builds the wrapper around the native callTracer. The
// TracerConfig is forwarded to the inner callTracer unchanged.
func newParityCallTracer(ctx *Context, cfg json.RawMessage, chainConfig *params.ChainConfig) (*Tracer, error) {
	inner, err := DefaultDirectory.New("callTracer", ctx, cfg, chainConfig)
	if err != nil {
		return nil, err
	}
	t := &parityCallTracer{inner: inner}

	// Copy the inner hooks and compose an OnGasChange that records the gas
	// remaining when execution ends. The first transaction-finalization event
	// (refunds, data floor, or leftover return) carries that value as its "old".
	hooks := *inner.Hooks
	innerOnGasChange := hooks.OnGasChange
	hooks.OnGasChange = func(old, new uint64, reason tracing.GasChangeReason) {
		if !t.gasLeftSet {
			switch reason {
			case tracing.GasChangeTxRefunds, tracing.GasChangeTxLeftOverReturned, tracing.GasChangeTxDataFloor:
				t.gasLeft = old
				t.gasLeftSet = true
			}
		}
		if innerOnGasChange != nil {
			innerOnGasChange(old, new, reason)
		}
	}

	return &Tracer{
		Hooks:     &hooks,
		GetResult: t.getResult,
		Stop:      inner.Stop,
	}, nil
}

// getResult returns the inner callTracer frame together with the gas remaining
// after execution.
func (t *parityCallTracer) getResult() (json.RawMessage, error) {
	frame, err := t.inner.GetResult()
	if err != nil {
		return nil, err
	}
	return json.Marshal(parityCallResult{Frame: frame, GasLeft: t.gasLeft, GasLeftSet: t.gasLeftSet})
}
