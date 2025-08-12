package statefull

import (
	"bytes"
	"math"
	"math/big"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// System Address
var systemAddress = common.HexToAddress("0xffffFFFfFFffffffffffffffFfFFFfffFFFfFFfE")

// ChainContext implements core.ChainContext to allow passing it as a chain context.
type ChainContext struct {
	Chain consensus.ChainHeaderReader
	Bor   consensus.Engine
}

func (c ChainContext) Engine() consensus.Engine {
	return c.Bor
}

func (c ChainContext) GetHeader(hash common.Hash, number uint64) *types.Header {
	return c.Chain.GetHeader(hash, number)
}

func (c ChainContext) Config() *params.ChainConfig {
	return c.Chain.Config()
}

// Callmsg implements core.Message to allow passing it as a transaction simulator.
type Callmsg struct {
	ethereum.CallMsg
}

func (m Callmsg) From() common.Address { return m.CallMsg.From }
func (m Callmsg) Nonce() uint64        { return 0 }
func (m Callmsg) CheckNonce() bool     { return false }
func (m Callmsg) To() *common.Address  { return m.CallMsg.To }
func (m Callmsg) GasPrice() *big.Int   { return m.CallMsg.GasPrice }
func (m Callmsg) Gas() uint64          { return m.CallMsg.Gas }
func (m Callmsg) Value() *big.Int      { return m.CallMsg.Value }
func (m Callmsg) Data() []byte         { return m.CallMsg.Data }

// Get System Message
func GetSystemMessage(toAddress common.Address, data []byte) Callmsg {
	return Callmsg{
		ethereum.CallMsg{
			From:     systemAddress,
			Gas:      math.MaxUint64 / 2,
			GasPrice: big.NewInt(0),
			Value:    big.NewInt(0),
			To:       &toAddress,
			Data:     data,
		},
	}
}

// Apply Message
func ApplyMessage(
	msg Callmsg,
	state vm.StateDB,
	header *types.Header,
	chainConfig *params.ChainConfig,
	chainContext core.ChainContext,
) (bool, uint64, error) {
	initialGas := msg.Gas()

	// Creates a new context to be used in the EVM environment.
	blockContext := core.NewEVMBlockContext(header, chainContext, &header.Coinbase)

	// Creates a new environment which holds all relevant information about the transaction and calling mechanisms.
	vmenv := vm.NewEVM(blockContext, state, chainConfig, vm.Config{})

	// nolint : contextcheck
	// Applies the transaction to the current state (included in the env).
	ret, gasLeft, err := vmenv.Call(
		msg.From(),
		*msg.To(),
		msg.Data(),
		msg.Gas(),
		uint256.NewInt(msg.Value().Uint64()),
		nil,
	)

	applied := true
	gasUsed := initialGas - gasLeft
	success := big.NewInt(0).SetBytes(ret)
	validatorContract := common.HexToAddress(chainConfig.Bor.ValidatorContract)

	// If success == 0 and msg.To() != validatorContractAddress, return and log error.
	// If msg.To() == validatorContractAddress, its committing a span and we don't get any return value.
	if success.Cmp(big.NewInt(0)) == 0 && !bytes.Equal((*msg.To()).Bytes(), validatorContract.Bytes()) {
		applied = false
		log.Error("Message execution failed on contract", "to", *msg.To(), "msgData", msg.Data(), "gasUsed", gasUsed)
	}

	// If there's error committing span, log it here. It won't be reported before because the return value is empty.
	if bytes.Equal((*msg.To()).Bytes(), validatorContract.Bytes()) && err != nil {
		applied = false
		log.Error("Message execution failed on contract", "to", *msg.To(), "err", err, "gasUsed", gasUsed)
	}

	// Update the state with pending changes.
	if err != nil {
		applied = false
		state.Finalise(true)
		log.Error("Error applying message", "to", *msg.To(), "err", err, "gasUsed", gasUsed)
	}

	return applied, gasUsed, nil
}

// Apply Bor Message
func ApplyBorMessage(vmenv *vm.EVM, msg Callmsg) (bool, *core.ExecutionResult, error) {
	initialGas := msg.Gas()

	// Applies the transaction to the current state (included in the env).
	ret, gasLeft, err := vmenv.Call(
		msg.From(),
		*msg.To(),
		msg.Data(),
		msg.Gas(),
		uint256.NewInt(msg.Value().Uint64()),
		nil,
	)

	applied := true
	gasUsed := initialGas - gasLeft

	// Update the state with pending changes.
	if err != nil {
		applied = false
		vmenv.StateDB.Finalise(true)
		log.Error("Error applying bor message", "to", *msg.To(), "err", err, "gasUsed", gasUsed)
	}

	return applied, &core.ExecutionResult{UsedGas: gasUsed, Err: err, ReturnData: ret}, nil
}
