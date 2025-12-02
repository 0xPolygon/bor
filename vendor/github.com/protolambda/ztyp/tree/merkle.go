package tree

// Merkleize with log(N) space allocation
func Merkleize(hasher HashFn, count uint64, limit uint64, leaf func(i uint64) Root) (out Root) {
	if count > limit {
		// merkleizing list that is too large, over limit
		count = limit
	}
	if limit == 0 {
		return
	}
	if limit == 1 {
		if count == 1 {
			out = leaf(0)
		}
		return
	}
	depth := CoverDepth(count)
	limitDepth := CoverDepth(limit)
	tmp := make([]Root, limitDepth+1, limitDepth+1)

	j := uint8(0)
	hArr := Root{}

	merge := func(i uint64) {
		// merge back up from bottom to top, as far as we can
		for j = 0; ; j++ {
			// stop merging when we are in the left side of the next combi
			if i&(uint64(1)<<j) == 0 {
				// if we are at the count, we want to merge in zero-hashes for padding
				if i == count && j < depth {
					hArr = hasher(hArr, ZeroHashes[j])
				} else {
					break
				}
			} else {
				// keep merging up if we are the right side
				hArr = hasher(tmp[j], hArr)
			}
		}
		// store the merge result (may be no merge, i.e. bottom leaf node)
		tmp[j] = hArr
	}

	// merge in leaf by leaf.
	for i := uint64(0); i < count; i++ {
		hArr = leaf(i)
		merge(i)
	}

	// complement with 0 if empty, or if not the right power of 2
	if (uint64(1) << depth) != count {
		hArr = ZeroHashes[0]
		merge(count)
	}

	// the next power of two may be smaller than the ultimate virtual size,
	// complement with zero-hashes at each depth.
	for j := depth; j < limitDepth; j++ {
		tmp[j+1] = hasher(tmp[j], ZeroHashes[j])
	}

	return tmp[limitDepth]
}
