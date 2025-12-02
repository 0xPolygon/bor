package phase0

import (
	"context"
	"errors"

	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"
)

type Eth1DataVotes []common.Eth1Data

func (a *Eth1DataVotes) Deserialize(spec *common.Spec, dr *codec.DecodingReader) error {
	return dr.List(func() codec.Deserializable {
		i := len(*a)
		*a = append(*a, common.Eth1Data{})
		return &(*a)[i]
	}, common.Eth1DataType.TypeByteLength(), uint64(spec.EPOCHS_PER_ETH1_VOTING_PERIOD)*uint64(spec.SLOTS_PER_EPOCH))
}

func (a Eth1DataVotes) Serialize(spec *common.Spec, w *codec.EncodingWriter) error {
	return w.List(func(i uint64) codec.Serializable {
		return &a[i]
	}, common.Eth1DataType.TypeByteLength(), uint64(len(a)))
}

func (a Eth1DataVotes) ByteLength(spec *common.Spec) (out uint64) {
	return uint64(len(a)) * common.Eth1DataType.TypeByteLength()
}

func (a *Eth1DataVotes) FixedLength(spec *common.Spec) uint64 {
	return 0 // it's a list, no fixed length
}

func (li Eth1DataVotes) HashTreeRoot(spec *common.Spec, hFn tree.HashFn) common.Root {
	length := uint64(len(li))
	return hFn.ComplexListHTR(func(i uint64) tree.HTR {
		if i < length {
			return &li[i]
		}
		return nil
	}, length, uint64(spec.EPOCHS_PER_ETH1_VOTING_PERIOD)*uint64(spec.SLOTS_PER_EPOCH))
}

func Eth1DataVotesType(spec *common.Spec) ListTypeDef {
	return ListType(common.Eth1DataType, uint64(spec.EPOCHS_PER_ETH1_VOTING_PERIOD)*uint64(spec.SLOTS_PER_EPOCH))
}

type Eth1DataVotesView struct{ *ComplexListView }

var _ common.Eth1DataVotes = (*Eth1DataVotesView)(nil)

func AsEth1DataVotes(v View, err error) (*Eth1DataVotesView, error) {
	c, err := AsComplexList(v, err)
	return &Eth1DataVotesView{c}, err
}

// Done at the end of every voting period
func (v *Eth1DataVotesView) Reset() error {
	return v.SetBacking(v.Type().DefaultNode())
}

func (v *Eth1DataVotesView) Length() (uint64, error) {
	return v.ComplexListView.Length()
}

func (v *Eth1DataVotesView) Count(dat common.Eth1Data) (uint64, error) {
	count := uint64(0)
	iter := v.ReadonlyIter()
	hFn := tree.GetHashFn()
	voteRoot := dat.HashTreeRoot(hFn)
	for {
		existingVote, ok, err := iter.Next()
		if err != nil {
			return 0, err
		}
		if !ok {
			break
		}
		if existingVote.HashTreeRoot(hFn) == voteRoot {
			count++
		}
	}
	return count, nil
}

func (v *Eth1DataVotesView) Append(dat common.Eth1Data) error {
	return v.ComplexListView.Append(dat.View())
}

func ProcessEth1Vote(ctx context.Context, spec *common.Spec, epc *common.EpochsContext, state common.BeaconState, data common.Eth1Data) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	votes, err := state.Eth1DataVotes()
	if err != nil {
		return err
	}
	voteCount, err := votes.Length()
	if err != nil {
		return err
	}
	period := uint64(spec.EPOCHS_PER_ETH1_VOTING_PERIOD) * uint64(spec.SLOTS_PER_EPOCH)
	if voteCount >= period {
		return errors.New("cannot process Eth1 vote, already voted maximum times")
	}
	if err := votes.Append(data); err != nil {
		return err
	}
	voteCount += 1
	// only do costly counting if we have enough votes yet.
	if voteCount<<1 > period {
		count, err := votes.Count(data)
		if err != nil {
			return err
		}
		if (count << 1) > period {
			return state.SetEth1Data(data)
		}
	}
	return nil
}
