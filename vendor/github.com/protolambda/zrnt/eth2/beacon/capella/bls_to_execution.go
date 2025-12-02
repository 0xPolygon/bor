package capella

import (
	"bytes"
	"context"
	"fmt"

	blsu "github.com/protolambda/bls12-381-util"
	"github.com/protolambda/ztyp/tree"

	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/zrnt/eth2/util/hashing"
)

func ProcessBLSToExecutionChanges(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, state common.BeaconState, ops common.SignedBLSToExecutionChanges) error {
	for i := range ops {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := ProcessBLSToExecutionChange(ctx, spec, epc, state, &ops[i]); err != nil {
			return err
		}
	}
	return nil
}

func ProcessBLSToExecutionChange(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, state common.BeaconState, op *common.SignedBLSToExecutionChange) error {
	validators, err := state.Validators()
	if err != nil {
		return err
	}
	validatorCount, err := validators.ValidatorCount()
	if err != nil {
		return err
	}

	addressChange := op.BLSToExecutionChange
	if uint64(addressChange.ValidatorIndex) >= validatorCount {
		return fmt.Errorf("invalid validator index for bls to execution change")
	}

	validator, err := validators.Validator(addressChange.ValidatorIndex)
	if err != nil {
		return err
	}

	validatorWithdrawalCredentials, err := validator.WithdrawalCredentials()
	if err != nil {
		return err
	}
	if !bytes.Equal(validatorWithdrawalCredentials[:1], []byte{common.BLS_WITHDRAWAL_PREFIX}) {
		return fmt.Errorf("invalid bls to execution change, validator not bls: %v", validatorWithdrawalCredentials)
	}
	sigHash := hashing.Hash(addressChange.FromBLSPubKey[:])
	if !bytes.Equal(validatorWithdrawalCredentials[1:], sigHash[1:]) {
		return fmt.Errorf("invalid bls to execution change, incorrect public key: got %v, want %v", addressChange.FromBLSPubKey, validatorWithdrawalCredentials)
	}
	genesisValidatorsRoot, err := state.GenesisValidatorsRoot()
	if err != nil {
		return err
	}
	domain := common.ComputeDomain(common.DOMAIN_BLS_TO_EXECUTION_CHANGE, spec.GENESIS_FORK_VERSION, genesisValidatorsRoot)

	sigRoot := common.ComputeSigningRoot(addressChange.HashTreeRoot(tree.GetHashFn()), domain)
	pubKey, err := addressChange.FromBLSPubKey.Pubkey()
	if err != nil {
		return err
	}

	signature, err := op.Signature.Signature()
	if err != nil {
		return err
	}

	if !blsu.Verify(pubKey, sigRoot[:], signature) {
		return fmt.Errorf("invalid bls to execution change signature")
	}
	var newWithdrawalCredentials tree.Root
	copy(newWithdrawalCredentials[0:1], []byte{common.ETH1_ADDRESS_WITHDRAWAL_PREFIX})
	copy(newWithdrawalCredentials[12:], addressChange.ToExecutionAddress[:])
	return validator.SetWithdrawalCredentials(newWithdrawalCredentials)
}
