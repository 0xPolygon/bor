package tree

const (
	mask0 = ^uint64((1 << (1 << iota)) - 1)
	mask1
	mask2
	mask3
	mask4
	mask5
)

const (
	bit0 = uint8(1 << iota)
	bit1
	bit2
	bit3
	bit4
	bit5
)

// bitmagic: binary search through a uint64, to find the BitIndex of next power of 2 (if not already a power of 2)
// Zero is a special case, it has a 0 depth.
// Example:
//  (in out): (0 0), (1 0), (2 1), (3 2), (4 2), (5 3), (6 3), (7 3), (8 3), (9 4)
func CoverDepth(v uint64) (out uint8) {
	if v == 0 || v == 1 {
		return 0
	}
	return BitIndex(v-1) + 1
}

// bitmagic: binary search through a uint64 to find the bit-length
// Zero is a special case, it has a 0 bit length.
// Example:
//  (in out): (0 0), (1 1), (2 2), (3 2), (4 3), (5 3), (6 3), (7 3), (8 4), (9 4)
func BitLength(v uint64) (out uint8) {
	if v == 0 {
		return 0
	}
	return BitIndex(v) + 1
}

// bitmagic: binary search through a uint64 to find the index (least bit being 0) of the first set bit.
// Zero is a special case, it has a 0 bit index.
// Example:
//  (in out): (0 0), (1 0), (2 1), (3 1), (4 2), (5 2), (6 2), (7 2), (8 3), (9 3)
func BitIndex(v uint64) (out uint8) {
	if v == 0 {
		return 0
	}
	if v&mask5 != 0 {
		v >>= bit5
		out |= bit5
	}
	if v&mask4 != 0 {
		v >>= bit4
		out |= bit4
	}
	if v&mask3 != 0 {
		v >>= bit3
		out |= bit3
	}
	if v&mask2 != 0 {
		v >>= bit2
		out |= bit2
	}
	if v&mask1 != 0 {
		v >>= bit1
		out |= bit1
	}
	if v&mask0 != 0 {
		out |= bit0
	}
	return
}
