package core

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/blockstm"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/triedb"
	"github.com/holiman/uint256"
)

// A transaction involving a selfdestruct and a transfer makes BlockSTM V2 execution differ from
// serial execution. Both state roots are identical, but the receipt hash of the BlockSTM V2 differs.
// The receipt hash differs because a wrong log is emitted in the BlockSTM V2 case. This is Bor specific
// transfer (LogTransfer event) emitted for native value transfers.

// Note that the bloom matches, because
// serial      -> LogTransfer: token=0x0000000000000000000000000000000000001010 from=0x000000000000000000000000000000000000aaaa to=0x000000000000000000000000000000000000BbBB amount=10 input1=10 input2=10 output1=0 output2=20
// BlockSTM V2 -> LogTransfer: token=0x0000000000000000000000000000000000001010 from=0x000000000000000000000000000000000000aaaa to=0x000000000000000000000000000000000000BbBB amount=10 input1=10 input2=0 output1=0 output2=10

// Run:
// go test ./core/ -run TestV2_SelfDestructTransferLog_MispairsWithSerial -v
func TestV2_SelfDestructTransferLog_MispairsWithSerial(t *testing.T) {

	memdb := rawdb.NewMemoryDatabase()
	tdb := triedb.NewDatabase(memdb, triedb.HashDefaults)
	gen, _ := state.New(common.Hash{}, state.NewDatabase(tdb, nil))
	cfg := *params.MergedTestChainConfig

	// x Smart Contract. Simple code with two paths. Selft destruct to Y or call z contract. This contract is the entry point.
	//   if (msg.sender == Z) { selfdestruct(payable(Y)); }
	//   else { Z.call{value: 0}(""); payable(Y).transfer(A); }
	x := common.HexToAddress("0x000000000000000000000000000000000000aaaa")
	xCode := common.FromHex("3373000000000000000000000000000000000000cccc146056575f5f5f5f5f73000000000000000000000000000000000000cccc5af1505f5f5f5f600a73000000000000000000000000000000000000bbbb5af150005b73000000000000000000000000000000000000bbbbff")

	// y is just an EOA. Hardcoded above.
	// 0x000000000000000000000000000000000000bbbb

	// z Smart Contract. Calls X and selfdestructs itself.
	//   X.call{value: 0}(""); selfdestruct(payable(X));
	z := common.HexToAddress("0x000000000000000000000000000000000000cccc")
	zCode := common.FromHex("5f5f5f5f5f73000000000000000000000000000000000000aaaa5af15073000000000000000000000000000000000000aaaaff")

	weiToTransfer := uint64(10)
	gen.SetCode(x, xCode, tracing.CodeChangeUnspecified)
	gen.AddBalance(x, uint256.NewInt(weiToTransfer), tracing.BalanceChangeUnspecified)
	gen.SetCode(z, zCode, tracing.CodeChangeUnspecified)
	gen.AddBalance(z, uint256.NewInt(weiToTransfer), tracing.BalanceChangeUnspecified)

	key, _ := crypto.GenerateKey()
	sender := crypto.PubkeyToAddress(key.PublicKey)
	gen.AddBalance(sender, uint256.NewInt(1e18), tracing.BalanceChangeUnspecified)

	root, _ := gen.Commit(0, false, false)
	tdb.Commit(root, false)

	signer := types.NewLondonSigner(cfg.ChainID)

	// Note x contract is the entry point
	tx, _ := types.SignTx(types.NewTx(&types.DynamicFeeTx{
		ChainID:   cfg.ChainID,
		Nonce:     0,
		GasTipCap: big.NewInt(0),
		GasFeeCap: big.NewInt(7),
		Gas:       1_000_000,
		To:        &x,
		Value:     big.NewInt(0),
	}), signer, key)
	msg, _ := TransactionToMessage(tx, signer, big.NewInt(7))

	blockCtx := vm.BlockContext{
		CanTransfer: CanTransfer,
		Transfer:    Transfer,
		GetHash:     func(uint64) common.Hash { return common.Hash{} },
		Coinbase:    common.HexToAddress("0x00000000000000000000000000000000c0ffee00"),
		GasLimit:    30_000_000,
		BlockNumber: big.NewInt(1),
		Time:        1,
		BaseFee:     big.NewInt(7),
		Random:      &common.Hash{},
	}

	// Serial Execution
	serialDB, _ := state.New(root, state.NewDatabase(tdb, nil))
	serialDB.SetTxContext(tx.Hash(), 0)
	serialEVM := vm.NewEVM(blockCtx, serialDB, &cfg, vm.Config{})
	usedGas := uint64(0)
	serialReceipt, err := ApplyTransactionWithEVM(msg, new(GasPool).AddGas(blockCtx.GasLimit),
		serialDB, blockCtx.BlockNumber, common.Hash{}, blockCtx.Time, tx, &usedGas, serialEVM)
	if err != nil {
		t.Fatalf("serial ApplyTransactionWithEVM: %v", err)
	}

	// BlockSTM V2 Execution
	v2DB, _ := state.New(root, state.NewDatabase(tdb, nil))
	v2DB.SetTxContext(tx.Hash(), 0)
	readBase := v2DB.Copy()
	readBase.EnableConcurrentReads()
	res := ExecuteV2BlockSTM(context.Background(), []V2Task{{Index: 0, Tx: tx, Msg: msg}},
		readBase, blockstm.NewMVStore(), blockstm.NewMVBalanceStore(),
		blockCtx, common.Hash{}, vm.Config{}, &cfg, blockCtx.GasLimit, 1, v2DB, nil)
	v2DB.Finalise(true)

	serialLog := decodeTransferLog(t, serialReceipt)
	v2Log := decodeTransferLog(t, res.Receipts[0])

	t.Logf("serial LogTransfer: %s", serialLog)
	t.Logf("v2     LogTransfer: %s", v2Log)

	if serialLog.data() != v2Log.data() {
		t.Errorf("V2 BlockSTM receipt diverges from serial for the same transaction")
	}
}

// Bor's native LogTransfer event
const logTransferABI = `[{"anonymous":false,"name":"LogTransfer","type":"event","inputs":[
  {"indexed":true, "name":"token",  "type":"address"},
  {"indexed":true, "name":"from",   "type":"address"},
  {"indexed":true, "name":"to",     "type":"address"},
  {"indexed":false,"name":"amount", "type":"uint256"},
  {"indexed":false,"name":"input1", "type":"uint256"},
  {"indexed":false,"name":"input2", "type":"uint256"},
  {"indexed":false,"name":"output1","type":"uint256"},
  {"indexed":false,"name":"output2","type":"uint256"}]}]`

// transferLog is a decoded LogTransfer. Official Polygon Bor native transfers.
type transferLog struct {
	Token, From, To                          common.Address
	Amount, Input1, Input2, Output1, Output2 *big.Int
}

func (l transferLog) String() string {
	return fmt.Sprintf("token=%s from=%s to=%s amount=%d input1=%d input2=%d output1=%d output2=%d",
		l.Token.Hex(), l.From.Hex(), l.To.Hex(), l.Amount, l.Input1, l.Input2, l.Output1, l.Output2)
}

// data returns the 5 non-indexed words for a simple (==) comparison.
func (l transferLog) data() [5]uint64 {
	return [5]uint64{l.Amount.Uint64(), l.Input1.Uint64(), l.Input2.Uint64(), l.Output1.Uint64(), l.Output2.Uint64()}
}

// decodeTransferLog finds the Bor LogTransfer in a receipt and ABI-decodes it
// (indexed topics + data) into named fields via bind.UnpackLog -- no manual
// byte slicing.
func decodeTransferLog(t *testing.T, r *types.Receipt) transferLog {
	t.Helper()
	parsed, err := abi.JSON(strings.NewReader(logTransferABI))
	if err != nil {
		t.Fatalf("parse LogTransfer ABI: %v", err)
	}
	bc := bind.NewBoundContract(feeAddress, parsed, nil, nil, nil)
	for _, l := range r.Logs {
		if l.Address == feeAddress && len(l.Topics) > 0 && l.Topics[0] == transferLogSig {
			var e transferLog
			if err := bc.UnpackLog(&e, "LogTransfer", *l); err != nil {
				t.Fatalf("UnpackLog: %v", err)
			}
			return e
		}
	}
	t.Fatalf("no Bor LogTransfer (0x%x at %s) found in receipt %s", transferLogSig, feeAddress, r.TxHash)
	return transferLog{}
}
