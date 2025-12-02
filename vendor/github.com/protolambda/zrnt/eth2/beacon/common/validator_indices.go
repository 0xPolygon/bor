package common

import (
	"fmt"
	"sort"

	"github.com/protolambda/ztyp/codec"
	"github.com/protolambda/ztyp/tree"
	. "github.com/protolambda/ztyp/view"
)

const ValidatorIndexType = Uint64Type

// Index of a validator, pointing to a validator registry location
type ValidatorIndex Uint64View

func AsValidatorIndex(v View, err error) (ValidatorIndex, error) {
	i, err := AsUint64(v, err)
	return ValidatorIndex(i), err
}

func (i *ValidatorIndex) Deserialize(dr *codec.DecodingReader) error {
	return (*Uint64View)(i).Deserialize(dr)
}

func (i ValidatorIndex) Serialize(w *codec.EncodingWriter) error {
	return w.WriteUint64(uint64(i))
}

func (ValidatorIndex) ByteLength() uint64 {
	return 8
}

func (ValidatorIndex) FixedLength() uint64 {
	return 8
}

func (i ValidatorIndex) HashTreeRoot(hFn tree.HashFn) Root {
	return Uint64View(i).HashTreeRoot(hFn)
}

func (e ValidatorIndex) MarshalJSON() ([]byte, error) {
	return Uint64View(e).MarshalJSON()
}

func (e *ValidatorIndex) UnmarshalJSON(b []byte) error {
	return ((*Uint64View)(e)).UnmarshalJSON(b)
}

func (e ValidatorIndex) String() string {
	return Uint64View(e).String()
}

// Custom constant, not in spec:
// An impossible high validator index used to mark special internal cases. (all 1s binary)
const ValidatorIndexMarker = ValidatorIndex(^uint64(0))

// Collection of validators, should always be sorted.
type ValidatorSet []ValidatorIndex

func (vs ValidatorSet) Len() int {
	return len(vs)
}

func (vs ValidatorSet) Less(i int, j int) bool {
	return vs[i] < vs[j]
}

func (vs ValidatorSet) Swap(i int, j int) {
	vs[i], vs[j] = vs[j], vs[i]
}

// De-duplicates entries in the set in-place. (util to make a valid set from a list with duplicates)
func (vs *ValidatorSet) Dedup() {
	data := *vs
	if len(data) == 0 {
		return
	}
	sort.Sort(vs)
	j := 0
	for i := 1; i < len(data); i++ {
		if data[j] == data[i] {
			continue
		}
		j++
		data[j] = data[i]
	}
	*vs = data[:j+1]
}

// merges with other disjoint set, producing a new set.
func (vs ValidatorSet) MergeDisjoint(other ValidatorSet) ValidatorSet {
	total := len(vs) + len(other)
	out := make(ValidatorSet, 0, total)
	a, b := 0, 0
	for i := 0; i < total; i++ {
		if a < len(vs) {
			if b >= len(other) || vs[a] < other[b] {
				out = append(out, vs[a])
				a++
				continue
			} else if vs[a] == other[b] {
				panic("invalid disjoint sets merge, sets contain equal item")
			}
		}
		if b < len(other) {
			if b < len(other) && (a >= len(vs) || vs[a] > other[b]) {
				out = append(out, other[b])
				b++
				continue
			}
		}
	}
	return out
}

// Filter the set in-place. Retains every validator for which the function returns true.
func (vs *ValidatorSet) Filter(retain func(index ValidatorIndex) (bool, error)) error {
	if vs == nil {
		return nil
	}
	j := 0
	vsr := *vs
	for i := 0; i < len(vsr); i++ {
		if res, err := retain(vsr[i]); err != nil {
			return fmt.Errorf("validator %d failed check", vsr[i])
		} else if res {
			vsr[j] = vsr[i]
			j++
		}
	}
	*vs = vsr[:j]
	return nil
}

// Joins two validator sets: check if there is any overlap
func (vs ValidatorSet) Intersects(target ValidatorSet) bool {
	// index for source set side of the zig-zag
	i := 0
	// index for target set side of the zig-zag
	j := 0
	var iV, jV ValidatorIndex
	updateI := func() {
		// if out of bounds, just update to an impossibly high index
		if i < len(vs) {
			iV = vs[i]
		} else {
			iV = ValidatorIndexMarker
		}
	}
	updateJ := func() {
		// if out of bounds, just update to an impossibly high index
		if j < len(target) {
			jV = target[j]
		} else {
			jV = ValidatorIndexMarker
		}
	}
	updateI()
	updateJ()
	for {
		// at some point all items in vs have been processed.
		if i >= len(vs) {
			break
		}
		if iV == jV {
			return true
		} else if iV < jV {
			// go to next
			i++
			updateI()
		} else if iV > jV {
			// if the index is higher than the current item in the target, go to the next item.
			j++
			updateJ()
		}
	}
	return false
}

// Joins two validator sets:
// reports all indices of source that are in the target (call onIn), and those that are not (call onOut)
func (vs ValidatorSet) ZigZagJoin(target ValidatorSet, onIn func(i ValidatorIndex), onOut func(i ValidatorIndex)) {
	// index for source set side of the zig-zag
	i := 0
	// index for target set side of the zig-zag
	j := 0
	var iV, jV ValidatorIndex
	updateI := func() {
		// if out of bounds, just update to an impossibly high index
		if i < len(vs) {
			iV = vs[i]
		} else {
			iV = ValidatorIndexMarker
		}
	}
	updateJ := func() {
		// if out of bounds, just update to an impossibly high index
		if j < len(target) {
			jV = target[j]
		} else {
			jV = ValidatorIndexMarker
		}
	}
	updateI()
	updateJ()
	for {
		// at some point all items in vs have been processed.
		if i >= len(vs) {
			break
		}
		if iV == jV {
			if onIn != nil {
				onIn(iV)
			}
			// go to next
			i++
			updateI()
			j++
			updateJ()
		} else if iV < jV {
			// if the index is lower than the current item in the target, it's not in the target.
			if onOut != nil {
				onOut(iV)
			}
			// go to next
			i++
			updateI()
		} else if iV > jV {
			// if the index is higher than the current item in the target, go to the next item.
			j++
			updateJ()
		}
	}
}
