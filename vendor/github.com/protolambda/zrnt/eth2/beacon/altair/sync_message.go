package altair

import (
	"errors"
	"fmt"

	blsu "github.com/protolambda/bls12-381-util"
	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"
)

type SyncCommitteeMessage struct {
	// Slot to which this contribution pertains
	Slot common.Slot `yaml:"slot" json:"slot"`
	// Block root for this signature
	BeaconBlockRoot common.Root `yaml:"beacon_block_root" json:"beacon_block_root"`
	// Index of the validator that produced this signature
	ValidatorIndex common.ValidatorIndex `yaml:"validator_index" json:"validator_index"`
	// Signature by the validator over the block root of `slot`
	Signature common.BLSSignature `yaml:"signature" json:"signature"`
}

var SyncCommitteeMessageType = ContainerType("SyncCommitteeMessage", []FieldDef{
	{"slot", common.SlotType},
	{"beacon_block_root", RootType},
	{"validator_index", common.ValidatorIndexType},
	{"signature", common.BLSSignatureType},
})

func (msg *SyncCommitteeMessage) Deserialize(dr *codec.DecodingReader) error {
	return dr.FixedLenContainer(
		&msg.Slot,
		&msg.BeaconBlockRoot,
		&msg.ValidatorIndex,
		&msg.Signature,
	)
}

func (msg *SyncCommitteeMessage) Serialize(w *codec.EncodingWriter) error {
	return w.FixedLenContainer(
		&msg.Slot,
		&msg.BeaconBlockRoot,
		&msg.ValidatorIndex,
		&msg.Signature,
	)
}

func (msg *SyncCommitteeMessage) ByteLength() uint64 {
	return codec.ContainerLength(
		&msg.Slot,
		&msg.BeaconBlockRoot,
		&msg.ValidatorIndex,
		&msg.Signature,
	)
}

func (msg *SyncCommitteeMessage) FixedLength() uint64 {
	return codec.ContainerLength(
		&msg.Slot,
		&msg.BeaconBlockRoot,
		&msg.ValidatorIndex,
		&msg.Signature,
	)
}

func (msg *SyncCommitteeMessage) HashTreeRoot(hFn tree.HashFn) common.Root {
	return hFn.HashTreeRoot(
		&msg.Slot,
		&msg.BeaconBlockRoot,
		&msg.ValidatorIndex,
		&msg.Signature,
	)
}

func (msg *SyncCommitteeMessage) VerifySignature(spec *common.Spec, epc *common.EpochsContext, domFn common.BLSDomainFn) error {
	pub, ok := epc.ValidatorPubkeyCache.Pubkey(msg.ValidatorIndex)
	if !ok {
		return fmt.Errorf("could not fetch pubkey for sync committee member %d", msg.ValidatorIndex)
	}
	blsPub, err := pub.Pubkey()
	if err != nil {
		return err
	}
	dom, err := domFn(common.DOMAIN_SYNC_COMMITTEE, spec.SlotToEpoch(msg.Slot))
	if err != nil {
		return err
	}
	signingRoot := common.ComputeSigningRoot(msg.BeaconBlockRoot, dom)
	sig, err := msg.Signature.Signature()
	if err != nil {
		return fmt.Errorf("failed to deserialize and sub-group check individual sync committee contribution signature: %v", err)
	}
	if !blsu.Verify(blsPub, signingRoot[:], sig) {
		return errors.New("could not verify BLS signature for individual sync committee contribution")
	}
	return nil
}

type SyncCommitteeMessageView struct {
	*ContainerView
}

func AsSyncCommitteeMessage(v View, err error) (*SyncCommitteeMessageView, error) {
	c, err := AsContainer(v, err)
	return &SyncCommitteeMessageView{c}, err
}
