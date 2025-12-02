package phase0

import (
	"errors"

	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"
)

var DepositRootsType = ComplexListType(RootType, 1<<common.DEPOSIT_CONTRACT_TREE_DEPTH)

type DepositRootsView struct {
	*ComplexListView
}

func AsDepositRootsView(v View, err error) (*DepositRootsView, error) {
	c, err := AsComplexList(v, err)
	return &DepositRootsView{c}, err
}

func NewDepositRootsView() *DepositRootsView {
	return &DepositRootsView{DepositRootsType.New()}
}

func GenesisFromEth1(spec *common.Spec, eth1BlockHash common.Root, time common.Timestamp, deps []common.Deposit, ignoreSignaturesAndProofs bool) (*BeaconStateView, *common.EpochsContext, error) {
	state := NewBeaconStateView(spec)
	if err := state.SetGenesisTime(time + spec.GENESIS_DELAY); err != nil {
		return nil, nil, err
	}
	if err := state.SetFork(common.Fork{
		PreviousVersion: spec.GENESIS_FORK_VERSION,
		CurrentVersion:  spec.GENESIS_FORK_VERSION,
		Epoch:           common.GENESIS_EPOCH,
	}); err != nil {
		return nil, nil, err
	}
	eth1Dat := common.Eth1Data{
		DepositRoot:  common.Root{}, // incrementally overwritten during deposit processing
		DepositCount: common.DepositIndex(len(deps)),
		BlockHash:    eth1BlockHash,
	}
	if err := state.SetEth1Data(eth1Dat); err != nil {
		return nil, nil, err
	}
	emptyBody := BeaconBlockBody{}
	latestHeader := &common.BeaconBlockHeader{
		BodyRoot: emptyBody.HashTreeRoot(spec, tree.GetHashFn()),
	}
	if err := state.SetLatestBlockHeader(latestHeader); err != nil {
		return nil, nil, err
	}
	// Seed RANDAO with Eth1 entropy
	err := state.SeedRandao(spec, eth1BlockHash)
	if err != nil {
		return nil, nil, err
	}

	vals, err := state.Validators()
	if err != nil {
		return nil, nil, err
	}
	pc, err := common.NewPubkeyCache(vals)
	if err != nil {
		return nil, nil, err
	}
	// Create mostly empty epochs context. Just need the pubkey cache first
	epc := &common.EpochsContext{
		Spec:                 spec,
		ValidatorPubkeyCache: pc,
	}

	depRootsView := NewDepositRootsView()

	hFn := tree.GetHashFn()
	updateDepTreeRoot := func() error {
		eth1Dat, err := state.Eth1Data()
		if err != nil {
			return err
		}
		eth1Dat.DepositRoot = depRootsView.HashTreeRoot(hFn)
		return state.SetEth1Data(eth1Dat)
	}
	// Process deposits
	for i := range deps {
		depRoot := RootView(deps[i].Data.HashTreeRoot(tree.GetHashFn()))
		if err := depRootsView.Append(&depRoot); err != nil {
			return nil, nil, err
		}
		if err := updateDepTreeRoot(); err != nil {
			return nil, nil, err
		}
		// in the rare case someone tries to create a genesis block using invalid data, error.
		if err := ProcessDeposit(spec, epc, state, &deps[i], ignoreSignaturesAndProofs); err != nil {
			return nil, nil, err
		}
	}
	if err := updateDepTreeRoot(); err != nil {
		return nil, nil, err
	}
	// fetch validator registry again, the state changed.
	vals, err = state.Validators()
	if err != nil {
		return nil, nil, err
	}
	valCount, err := vals.ValidatorCount()
	if err != nil {
		return nil, nil, err
	}
	if common.Slot(valCount) < spec.SLOTS_PER_EPOCH {
		return nil, nil, errors.New("not enough validators to init full featured BeaconState")
	}
	bals, err := state.Balances()
	if err != nil {
		panic(err)
	}
	// Process activations
	for i := uint64(0); i < valCount; i++ {
		val, err := vals.Validator(common.ValidatorIndex(i))
		if err != nil {
			return nil, nil, err
		}
		balance, err := bals.GetBalance(common.ValidatorIndex(i))
		if err != nil {
			return nil, nil, err
		}
		vEff := balance - (balance % spec.EFFECTIVE_BALANCE_INCREMENT)
		if vEff > spec.MAX_EFFECTIVE_BALANCE {
			vEff = spec.MAX_EFFECTIVE_BALANCE
		}
		if err := val.SetEffectiveBalance(vEff); err != nil {
			return nil, nil, err
		}
		if vEff == spec.MAX_EFFECTIVE_BALANCE {
			if err := val.SetActivationEligibilityEpoch(common.GENESIS_EPOCH); err != nil {
				return nil, nil, err
			}
			if err := val.SetActivationEpoch(common.GENESIS_EPOCH); err != nil {
				return nil, nil, err
			}
		}
	}
	if err := state.SetGenesisValidatorsRoot(vals.HashTreeRoot(hFn)); err != nil {
		return nil, nil, err
	}
	// Complete computation of epc
	if err := epc.LoadShuffling(state); err != nil {
		return nil, nil, err
	}
	if err := epc.LoadProposers(state); err != nil {
		return nil, nil, err
	}
	return state, epc, nil
}

func IsValidGenesisState(spec *common.Spec, state common.BeaconState) (bool, error) {
	genTime, err := state.GenesisTime()
	if err != nil {
		return false, err
	}
	if genTime < spec.MIN_GENESIS_TIME {
		return false, nil
	}

	// outside of genesis we have this precomputed at all times. Just compute it manually this time.
	activeCount := uint64(0)
	{
		validators, err := state.Validators()
		if err != nil {
			return false, err
		}
		valIterNext := validators.Iter()
		for {
			val, ok, err := valIterNext()
			if err != nil {
				return false, err
			}
			if !ok {
				break
			}
			if active, err := IsActive(val, common.GENESIS_EPOCH); err != nil {
				return false, err
			} else if active {
				activeCount += 1
			}
		}
	}
	return activeCount >= uint64(spec.MIN_GENESIS_ACTIVE_VALIDATOR_COUNT), nil
}
