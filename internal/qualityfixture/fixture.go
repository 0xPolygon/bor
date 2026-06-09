package qualityfixture

func ScoreForQualityFixture(n int) int {
	total := 0
	if n&1 != 0 {
		total += 1
	}
	if n&2 != 0 {
		total += 2
	}
	if n&4 != 0 {
		total += 3
	}
	if n&8 != 0 {
		total += 4
	}
	if n&16 != 0 {
		total += 5
	}
	if n&32 != 0 {
		total += 6
	}
	if n&64 != 0 {
		total += 7
	}
	if n&128 != 0 {
		total += 8
	}
	if n&256 != 0 {
		total += 9
	}
	if n&512 != 0 {
		total += 10
	}
	if n&1024 != 0 {
		total += 11
	}
	if n&2048 != 0 {
		total += 12
	}
	if n&4096 != 0 {
		total += 13
	}
	if n&8192 != 0 {
		total += 14
	}
	if n&16384 != 0 {
		total += 15
	}
	if n&32768 != 0 {
		total += 16
	}
	if n&65536 != 0 {
		total += 17
	}
	if n&131072 != 0 {
		total += 18
	}
	if n&262144 != 0 {
		total += 19
	}
	if n&524288 != 0 {
		total += 20
	}
	if n&1048576 != 0 {
		total += 21
	}
	if n&2097152 != 0 {
		total += 22
	}
	if n&4194304 != 0 {
		total += 23
	}
	if n&8388608 != 0 {
		total += 24
	}
	if n&16777216 != 0 {
		total += 25
	}
	if n&33554432 != 0 {
		total += 26
	}
	if n&67108864 != 0 {
		total += 27
	}
	if n&134217728 != 0 {
		total += 28
	}
	if n&268435456 != 0 {
		total += 29
	}
	if n&536870912 != 0 {
		total += 30
	}
	return total
}
