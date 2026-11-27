package core

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"math/big"
	"math/rand"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/beacon"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/core/vm/program"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
)

// Random chain generator for TestPipelinedImportDifferential.

const (
	pifSenders = 8
	pifSalts   = 2
)

var (
	pifCoinbase = common.HexToAddress("0x7Ee41D8A25641000661B1EF5E6AE8A00400466B0")
	pifBurnt    = common.HexToAddress("0x3ef57def668054dd750bd260526105c4eeef104f")
	pifStore    = common.HexToAddress("0x00000000000000000000000000000000000057a0")
	pifExec     = common.HexToAddress("0x00000000000000000000000000000000000057e0")
	pifFactory  = common.HexToAddress("0x00000000000000000000000000000000000057f0")

	pifKeys = func() []*ecdsa.PrivateKey {
		keys := make([]*ecdsa.PrivateKey, pifSenders)
		for i := range keys {
			seed := sha256.Sum256([]byte{0x70, byte(i)})
			key, err := crypto.ToECDSA(seed[:])
			if err != nil {
				panic(err)
			}
			keys[i] = key
		}
		return keys
	}()

	// store: k = cd[0:32]; cd[32:64] != 0 clears slot k, otherwise increments
	// it; logs the new value.
	pifStoreCode = common.FromHex("602035601457" + "5f358054600101805f5fa1905500" + "5b5f5f3555" + "5f5f5fa100")
	// exec: CREATE(callvalue, calldata); log the address.
	pifExecCode = program.New().
			Op(vm.CALLDATASIZE).Push0().Push0().Op(vm.CALLDATACOPY).
			Op(vm.CALLDATASIZE).Push0().Op(vm.CALLVALUE).Op(vm.CREATE).
			Push0().Push0().Op(vm.LOG1).Bytes()
	// factory: CREATE2(callvalue, init = cd[32:], salt = cd[0:32]); log it.
	pifFactoryCode = program.New().
			Op(vm.CALLDATASIZE).Push0().Push0().Op(vm.CALLDATACOPY).
			Push0().Op(vm.MLOAD).Push(32).Op(vm.CALLDATASIZE).Op(vm.SUB).Push(32).
			Op(vm.CALLVALUE).Op(vm.CREATE2).Push0().Push0().Op(vm.LOG1).Bytes()
	// Factory children: selfdestruct in the constructor, selfdestruct when
	// called, and a child holding storage.
	pifChildInit = [][]byte{
		program.New().Op(vm.ORIGIN, vm.SELFDESTRUCT).Bytes(),
		program.New().ReturnViaCodeCopy([]byte{byte(vm.CALLER), byte(vm.SELFDESTRUCT)}).Bytes(),
		program.New().Sstore(0, 1).ReturnViaCodeCopy([]byte{byte(vm.SELFBALANCE), byte(vm.PUSH0), byte(vm.MSTORE), byte(vm.STOP)}).Bytes(),
	}
)

func pifSender(i int) common.Address { return crypto.PubkeyToAddress(pifKeys[i].PublicKey) }
func pifFresh(i int) common.Address  { return common.BigToAddress(big.NewInt(int64(0xf000 + i))) }

func pifChild(kind, salt int) common.Address {
	return crypto.CreateAddress2(pifFactory, common.Hash{31: byte(salt)}, crypto.Keccak256(pifChildInit[kind]))
}

func pifFactoryInput(kind, salt int) []byte {
	return append(common.Hash{31: byte(salt)}.Bytes(), pifChildInit[kind]...)
}

// pifChainConfig is MergedTestChainConfig with the mainnet fork set and the
// mainnet fee recipients (post-Rio coinbase, burnt contract).
func pifChainConfig() *params.ChainConfig {
	cfg := *params.MergedTestChainConfig
	bor := *cfg.Bor
	cfg.OsakaBlock, bor.HampiBlock = nil, nil
	bor.GiuglianoBlock, bor.AustinBlock = big.NewInt(0), big.NewInt(0)
	bor.BurntContract = map[string]string{"0": pifBurnt.Hex()}
	bor.Coinbase = map[string]string{"0": pifCoinbase.Hex()}
	cfg.Bor = &bor
	return &cfg
}

func pifGenesis(r *rand.Rand) *Genesis {
	rich := new(big.Int).Mul(big.NewInt(1e18), big.NewInt(1e6))
	alloc := types.GenesisAlloc{
		pifExec:    {Code: pifExecCode, Nonce: 1, Balance: big.NewInt(1000)},
		pifFactory: {Code: pifFactoryCode, Nonce: 1, Balance: big.NewInt(0)},
	}
	for i := range pifSenders {
		alloc[pifSender(i)] = types.Account{Balance: rich}
	}
	storage := map[common.Hash]common.Hash{}
	for k := range 3 {
		if r.Intn(2) == 0 {
			storage[common.Hash{31: byte(k)}] = common.Hash{31: byte(1 + r.Intn(200))}
		}
	}
	alloc[pifStore] = types.Account{Code: pifStoreCode, Nonce: 1, Balance: big.NewInt(0), Storage: storage}
	if bal := []int64{0, 2, 1e18}[r.Intn(3)]; bal > 0 {
		alloc[pifCoinbase] = types.Account{Balance: big.NewInt(bal)}
	}
	if r.Intn(2) == 0 {
		alloc[pifBurnt] = types.Account{Balance: big.NewInt(1e18)}
	}
	if r.Intn(2) == 0 {
		alloc[pifChild(r.Intn(3), r.Intn(pifSalts))] = types.Account{Balance: big.NewInt(9)}
	}
	if r.Intn(2) == 0 {
		alloc[pifFresh(r.Intn(4))] = types.Account{Balance: big.NewInt(1)}
	}
	return &Genesis{Config: pifChainConfig(), Alloc: alloc, GasLimit: 60_000_000, BaseFee: big.NewInt(params.InitialBaseFee)}
}

// pifTarget returns a random address the generated code can touch.
func pifTarget(r *rand.Rand) common.Address {
	switch r.Intn(8) {
	case 0:
		return pifCoinbase
	case 1:
		return pifBurnt
	case 2:
		return pifStore
	case 3:
		return pifSender(r.Intn(pifSenders))
	case 4:
		return pifFresh(r.Intn(4))
	default:
		return pifChild(r.Intn(3), r.Intn(pifSalts))
	}
}

// pifCode appends n random actions to p. Observed values are logged and
// stored in the executing contract's storage so they reach receipts and state.
func pifCode(r *rand.Rand, p *program.Program, n, depth int) {
	slot := 0
	record := func() {
		p.Op(vm.DUP1).Push0().Push0().Op(vm.LOG1).Push(slot).Op(vm.SSTORE)
		slot++
	}
	for range n {
		switch r.Intn(10) {
		case 0:
			p.Push(pifTarget(r)).Op(vm.BALANCE)
			record()
		case 1:
			p.Push(pifTarget(r)).Op([]vm.OpCode{vm.EXTCODEHASH, vm.EXTCODESIZE}[r.Intn(2)])
			record()
		case 2:
			p.Push0().Push0().Push0().Push0().Push([]int{0, 1, 7}[r.Intn(3)]).Push(pifTarget(r)).Push(60000).Op(vm.CALL)
			record()
		case 3:
			p.Push(r.Intn(3)).Push0().Op(vm.MSTORE).Push(r.Intn(2)).Push(32).Op(vm.MSTORE)
			p.Push0().Push0().Push(64).Push0().Push0().Push(pifStore).Push(80000).Op(vm.CALL)
			record()
		case 4:
			in := pifFactoryInput(r.Intn(3), r.Intn(pifSalts))
			p.Mstore(in, 0)
			p.Push0().Push0().Push(len(in)).Push0().Push(r.Intn(2) * 3).Push(pifFactory).Push(300000).Op(vm.CALL)
			record()
		case 5:
			p.Push(r.Intn(3)).Push(r.Intn(3)).Op(vm.TSTORE)
			p.Push(r.Intn(3)).Op(vm.TLOAD)
			record()
		case 6:
			child := program.New().Push(pifTarget(r)).Op(vm.SELFDESTRUCT).Bytes()
			p.Mstore(child, 0)
			p.Push(len(child)).Push0().Push(r.Intn(2) * 5).Op(vm.CREATE)
			record()
		case 7:
			p.Push(r.Intn(2)).Push(1000 + r.Intn(3)).Op(vm.SSTORE)
		case 8:
			if depth == 0 {
				continue
			}
			pifNestedCall(r, p, depth-1)
			record()
		default:
			p.Op(vm.SELFBALANCE)
			record()
		}
	}
}

// pifNestedCall runs random code inside a CREATE through exec, sometimes
// reverting it, and leaves the CALL result on the stack.
func pifNestedCall(r *rand.Rand, p *program.Program, depth int) {
	sub := program.New()
	pifCode(r, sub, 1+r.Intn(3), depth)
	if r.Intn(2) == 0 {
		sub.Push0().Push0().Op(vm.REVERT)
	}
	code := sub.Bytes()
	p.Mstore(code, 0)
	p.Push0().Push0().Push(len(code)).Push0().Push(r.Intn(2) * 3).Push(pifExec).Op(vm.GAS).Op(vm.CALL)
}

func pifInitcode(r *rand.Rand) []byte {
	p := program.New()
	pifCode(r, p, 1+r.Intn(6), 2)
	switch r.Intn(4) {
	case 0:
		p.Push(pifTarget(r)).Op(vm.SELFDESTRUCT)
	case 1:
		p.Push0().Push0().Op(vm.REVERT)
	}
	return p.Bytes()
}

// pifTx builds one signed tx; nonceOf returns a sender's current nonce.
func pifTx(r *rand.Rand, chainID *big.Int, signer types.Signer, baseFee *big.Int, nSenders int, nonceOf func(int) uint64) *types.Transaction {
	s := r.Intn(nSenders)
	var (
		to    *common.Address
		data  []byte
		value = big.NewInt(0)
		gas   = uint64(1_000_000)
	)
	switch k := r.Intn(10); {
	case k < 3:
		data, value = pifInitcode(r), big.NewInt([]int64{0, 10, 1000}[r.Intn(3)])
	case k < 6:
		data, value, to = pifInitcode(r), big.NewInt([]int64{0, 10}[r.Intn(2)]), &pifExec
	case k < 8:
		a := pifTarget(r)
		to, value, gas = &a, big.NewInt([]int64{0, 1, 1e9}[r.Intn(3)]), 100_000
	case k == 8:
		data, to, value, gas = pifFactoryInput(r.Intn(3), r.Intn(pifSalts)), &pifFactory, big.NewInt([]int64{0, 5}[r.Intn(2)]), 300_000
	default:
		in := make([]byte, 64)
		in[31], in[63] = byte(r.Intn(3)), byte(r.Intn(2))
		data, to, gas = in, &pifStore, 100_000
	}
	tip := []int64{0, 1, 1e9}[r.Intn(3)]
	feeCap := new(big.Int).Add(baseFee, big.NewInt(tip))
	var txdata types.TxData
	switch r.Intn(6) {
	case 0:
		txdata = &types.LegacyTx{Nonce: nonceOf(s), GasPrice: feeCap, Gas: gas, To: to, Value: value, Data: data}
	case 1:
		dst := pifSender(r.Intn(nSenders))
		if to != nil {
			dst = *to
		}
		txdata = &types.SetCodeTx{ChainID: uint256.MustFromBig(chainID), Nonce: nonceOf(s),
			GasTipCap: uint256.NewInt(uint64(tip)), GasFeeCap: uint256.MustFromBig(feeCap), Gas: gas + 50_000,
			To: dst, Value: uint256.MustFromBig(value), Data: data,
			AuthList: []types.SetCodeAuthorization{pifAuth(r, chainID, s, nSenders, nonceOf)}}
	default:
		var al types.AccessList
		if r.Intn(4) == 0 {
			al = types.AccessList{{Address: pifCoinbase}, {Address: pifBurnt}}
		}
		txdata = &types.DynamicFeeTx{ChainID: chainID, Nonce: nonceOf(s), GasTipCap: big.NewInt(tip),
			GasFeeCap: feeCap, Gas: gas, To: to, Value: value, Data: data, AccessList: al}
	}
	return types.MustSignNewTx(pifKeys[s], signer, txdata)
}

// pifAuth signs an EIP-7702 authorization; the authority may also be a sender
// in this chain, or the tx sender itself.
func pifAuth(r *rand.Rand, chainID *big.Int, sender, nSenders int, nonceOf func(int) uint64) types.SetCodeAuthorization {
	a := r.Intn(nSenders)
	nonce := nonceOf(a)
	if a == sender {
		nonce++
	}
	delegates := []common.Address{pifExec, pifFactory, pifStore, pifChild(1, 0), {}}
	auth, err := types.SignSetCode(pifKeys[a], types.SetCodeAuthorization{
		ChainID: *uint256.MustFromBig(chainID), Address: delegates[r.Intn(len(delegates))], Nonce: nonce})
	if err != nil {
		panic(err)
	}
	return auth
}

// pifTryAddTx skips a tx rejected by a pre-check (gas pool, nonce); those run
// before any state change.
func pifTryAddTx(gen *BlockGen, tx *types.Transaction) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	gen.AddTx(tx)
	return true
}

func pifGenerate(r *rand.Rand) (*Genesis, []*types.Block) {
	gspec := pifGenesis(r)
	nSenders := 1 + r.Intn(pifSenders)
	_, blocks, _ := GenerateChainWithGenesis(gspec, beacon.New(ethash.NewFaker()), 2+r.Intn(10), func(_ int, gen *BlockGen) {
		gen.SetPoS()
		gen.SetCoinbase(pifCoinbase) // what the miner resolves post-Rio
		signer := types.MakeSigner(gspec.Config, gen.Number(), gen.Timestamp())
		ntx := r.Intn(16)
		if r.Intn(4) == 0 {
			ntx = 0 // empty blocks shift the pipeline window
		}
		for range ntx {
			pifTryAddTx(gen, pifTx(r, gspec.Config.ChainID, signer, gen.BaseFee(), nSenders,
				func(i int) uint64 { return gen.TxNonce(pifSender(i)) }))
		}
	})
	return gspec, blocks
}
