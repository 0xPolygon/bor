package common

import "fmt"

// Step combines a Slot and bool for block processing being included or not.
type Step uint64

func AsStep(slot Slot, block bool) Step {
	if slot&(1<<63) != 0 {
		panic("slot overflow")
	}
	out := Step(slot) << 1
	if block {
		out++
	}
	return out
}

func (st Step) String() string {
	if st.Block() {
		return fmt.Sprintf("%d:1", st.Slot())
	} else {
		return fmt.Sprintf("%d:0", st.Slot())
	}
}

func (st Step) Slot() Slot {
	return Slot(st >> 1)
}

func (st Step) Block() bool {
	return st&1 != 0
}
