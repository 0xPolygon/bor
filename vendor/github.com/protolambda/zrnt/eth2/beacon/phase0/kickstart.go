package phase0

import (
	"errors"

	kbls "github.com/kilic/bls12-381"
	blsu "github.com/protolambda/bls12-381-util"
	"github.com/protolambda/zrnt/eth2/beacon/common"
)

type KickstartValidatorData struct {
	Pubkey                common.BLSPubkey
	WithdrawalCredentials common.Root
	Balance               common.Gwei
}

// To build a genesis state without Eth 1.0 deposits, i.e. directly from a sequence of minimal validator data.
func KickStartState(spec *common.Spec, eth1BlockHash common.Root, time common.Timestamp, validators []KickstartValidatorData) (*BeaconStateView, *common.EpochsContext, error) {
	deps := make([]common.Deposit, len(validators), len(validators))

	placeholderSig := common.BLSSignature((*blsu.Signature)(kbls.NewG2().One()).Serialize())
	for i := range validators {
		v := &validators[i]
		d := &deps[i]
		d.Data = common.DepositData{
			Pubkey:                v.Pubkey,
			WithdrawalCredentials: v.WithdrawalCredentials,
			Amount:                v.Balance,
			Signature:             placeholderSig,
		}
	}

	state, epc, err := GenesisFromEth1(spec, eth1BlockHash, 0, deps, true)
	if err != nil {
		return nil, nil, err
	}
	if err := state.SetGenesisTime(time); err != nil {
		return nil, nil, err
	}
	return state, epc, nil
}

// To build a genesis state without Eth 1.0 deposits, i.e. directly from a sequence of minimal validator data.
func KickStartStateWithSignatures(spec *common.Spec, eth1BlockHash common.Root, time common.Timestamp, validators []KickstartValidatorData, keys [][32]byte) (*BeaconStateView, *common.EpochsContext, error) {
	deps := make([]common.Deposit, len(validators), len(validators))

	for i := range validators {
		v := &validators[i]
		d := &deps[i]
		d.Data = common.DepositData{
			Pubkey:                v.Pubkey,
			WithdrawalCredentials: v.WithdrawalCredentials,
			Amount:                v.Balance,
			Signature:             common.BLSSignature{},
		}
		var secKey blsu.SecretKey
		if err := secKey.Deserialize(&keys[i]); err != nil {
			return nil, nil, err
		}
		dom := common.ComputeDomain(common.DOMAIN_DEPOSIT, spec.GENESIS_FORK_VERSION, common.Root{})
		msg := common.ComputeSigningRoot(d.Data.MessageRoot(), dom)
		sig := blsu.Sign(&secKey, msg[:])
		pub, err := blsu.SkToPk(&secKey)
		if err != nil {
			return nil, nil, err
		}
		p := common.BLSPubkey(pub.Serialize())
		if p != d.Data.Pubkey {
			return nil, nil, errors.New("privkey invalid, expected different pubkey")
		}
		d.Data.Signature = sig.Serialize()
	}

	state, epc, err := GenesisFromEth1(spec, eth1BlockHash, 0, deps, true)
	if err != nil {
		return nil, nil, err
	}
	if err := state.SetGenesisTime(time); err != nil {
		return nil, nil, err
	}
	return state, epc, nil
}
