package common

import (
	"errors"

	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/tree"
)

type GweiList []Gwei

func (a *GweiList) Deserialize(spec *Spec, dr *codec.DecodingReader) error {
	return dr.List(func() codec.Deserializable {
		i := len(*a)
		*a = append(*a, Gwei(0))
		return &(*a)[i]
	}, GweiType.TypeByteLength(), uint64(spec.VALIDATOR_REGISTRY_LIMIT))
}

func (a GweiList) Serialize(spec *Spec, w *codec.EncodingWriter) error {
	return w.List(func(i uint64) codec.Serializable {
		return &a[i]
	}, GweiType.TypeByteLength(), uint64(len(a)))
}

func (a GweiList) ByteLength(spec *Spec) (out uint64) {
	return uint64(len(a)) * GweiType.TypeByteLength()
}

func (a *GweiList) FixedLength(spec *Spec) uint64 {
	return 0 // it's a list, no fixed length
}

func (li GweiList) HashTreeRoot(spec *Spec, hFn tree.HashFn) Root {
	length := uint64(len(li))
	return hFn.Uint64ListHTR(func(i uint64) uint64 {
		return uint64(li[i])
	}, length, uint64(spec.VALIDATOR_REGISTRY_LIMIT))
}

type Deltas struct {
	Rewards   GweiList `json:"rewards" yaml:"rewards"`
	Penalties GweiList `json:"penalties" yaml:"penalties"`
}

func (a *Deltas) Deserialize(spec *Spec, dr *codec.DecodingReader) error {
	return dr.Container(spec.Wrap(&a.Rewards), spec.Wrap(&a.Penalties))
}

func (a *Deltas) Serialize(spec *Spec, w *codec.EncodingWriter) error {
	return w.Container(spec.Wrap(&a.Rewards), spec.Wrap(&a.Penalties))
}

func (a *Deltas) ByteLength(spec *Spec) uint64 {
	return 2*codec.OFFSET_SIZE + a.Rewards.ByteLength(spec) + a.Penalties.ByteLength(spec)
}

func (a *Deltas) FixedLength(*Spec) uint64 {
	return 0
}

func (a *Deltas) HashTreeRoot(spec *Spec, hFn tree.HashFn) Root {
	return hFn.HashTreeRoot(spec.Wrap(&a.Rewards), spec.Wrap(&a.Penalties))
}

func NewDeltas(validatorCount uint64) *Deltas {
	return &Deltas{
		Rewards:   make(GweiList, validatorCount, validatorCount),
		Penalties: make(GweiList, validatorCount, validatorCount),
	}
}

func (deltas *Deltas) Add(other *Deltas) {
	for i := 0; i < len(deltas.Rewards); i++ {
		deltas.Rewards[i] += other.Rewards[i]
	}
	for i := 0; i < len(deltas.Penalties); i++ {
		deltas.Penalties[i] += other.Penalties[i]
	}
}

func IncreaseBalance(v BalancesRegistry, index ValidatorIndex, delta Gwei) error {
	bal, err := v.GetBalance(index)
	if err != nil {
		return err
	}
	bal += delta
	return v.SetBalance(index, bal)
}

func DecreaseBalance(v BalancesRegistry, index ValidatorIndex, delta Gwei) error {
	bal, err := v.GetBalance(index)
	if err != nil {
		return err
	}
	// prevent underflow, clip to 0
	if bal >= delta {
		bal -= delta
	} else {
		bal = 0
	}
	return v.SetBalance(index, bal)
}

// Applies deltas to the balances in the state, returns the resulting balances,
// ready to overwrite the state balances subtree with.
func ApplyDeltas(state BeaconState, deltas *Deltas) ([]Gwei, error) {
	balances, err := state.Balances()
	if err != nil {
		return nil, err
	}
	length, err := balances.Length()
	if err != nil {
		return nil, err
	}
	if uint64(len(deltas.Penalties)) != length || uint64(len(deltas.Rewards)) != length {
		return nil, errors.New("cannot apply deltas to balances list with different length")
	}
	balancesOut := make([]Gwei, 0, length)
	balIterNext := balances.Iter()
	i := ValidatorIndex(0)
	for {
		bal, ok, err := balIterNext()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		bal += deltas.Rewards[i]
		if penalty := deltas.Penalties[i]; bal >= penalty {
			bal -= penalty
		} else {
			bal = 0
		}
		balancesOut = append(balancesOut, bal)
		i++
	}
	return balancesOut, nil
}
