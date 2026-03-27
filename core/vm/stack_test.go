package vm

import (
	"math/rand"
	"testing"

	"github.com/holiman/uint256"
)

func BenchmarkStackPushPop(b *testing.B) {
	stack := newstack()
	val := uint256.NewInt(0xdeadbeef)

	for b.Loop() {
		stack.push(val)
		stack.pop()
	}

	returnStack(stack)
}

func BenchmarkStackPush1024Pop1024(b *testing.B) {
	stack := newstack()
	val := uint256.NewInt(0xdeadbeef)

	for b.Loop() {
		for i := 0; i < 1024; i++ {
			stack.push(val)
		}
		for i := 0; i < 1024; i++ {
			stack.pop()
		}
	}

	returnStack(stack)
}

func BenchmarkStackPushPopInterleaved(b *testing.B) {
	stack := newstack()
	val := uint256.NewInt(0xdeadbeef)

	for b.Loop() {
		for i := 0; i < 512; i++ {
			stack.push(val)
			stack.push(val)
			stack.pop()
		}
		for i := 0; i < 512; i++ {
			stack.pop()
		}
	}

	returnStack(stack)
}

func BenchmarkStackPeek(b *testing.B) {
	stack := newstack()
	stack.push(uint256.NewInt(42))

	for b.Loop() {
		_ = stack.peek()
	}

	returnStack(stack)
}

func BenchmarkStackSwap(b *testing.B) {
	stack := newstack()
	for i := 0; i < 17; i++ {
		stack.push(uint256.NewInt(uint64(i)))
	}

	b.Run("swap1", func(b *testing.B) {
		for b.Loop() {
			stack.swap1()
		}
	})
	b.Run("swap4", func(b *testing.B) {
		for b.Loop() {
			stack.swap4()
		}
	})
	b.Run("swap8", func(b *testing.B) {
		for b.Loop() {
			stack.swap8()
		}
	})
	b.Run("swap16", func(b *testing.B) {
		for b.Loop() {
			stack.swap16()
		}
	})

	returnStack(stack)
}

func BenchmarkStackDup(b *testing.B) {
	stack := newstack()
	for i := 0; i < 16; i++ {
		stack.push(uint256.NewInt(uint64(i)))
	}

	b.Run("dup1", func(b *testing.B) {
		for b.Loop() {
			stack.dup(1)
			stack.pop()
		}
	})
	b.Run("dup8", func(b *testing.B) {
		for b.Loop() {
			stack.dup(8)
			stack.pop()
		}
	})
	b.Run("dup16", func(b *testing.B) {
		for b.Loop() {
			stack.dup(16)
			stack.pop()
		}
	})

	returnStack(stack)
}

// BenchmarkStackRandomWorkload simulates a realistic EVM-like stack workload:
// random mix of push, pop, peek, swap, and dup at varying stack depths.
func BenchmarkStackRandomWorkload(b *testing.B) {
	rng := rand.New(rand.NewSource(42))
	val := uint256.NewInt(0xcafebabe)

	// Pre-generate a sequence of operations to avoid RNG overhead in the hot loop.
	// 0=push, 1=pop, 2=peek, 3=swap1, 4=dup1
	const seqLen = 10000
	ops := make([]byte, seqLen)
	for i := range ops {
		ops[i] = byte(rng.Intn(5))
	}

	stack := newstack()

	for b.Loop() {
		for stack.len() > 0 {
			stack.pop()
		}
		stack.push(val)

		for _, op := range ops {
			switch op {
			case 0:
				if stack.len() < 1023 {
					stack.push(val)
				}
			case 1:
				if stack.len() > 1 {
					stack.pop()
				}
			case 2:
				_ = stack.peek()
			case 3:
				if stack.len() >= 2 {
					stack.swap1()
				}
			case 4:
				if stack.len() >= 1 && stack.len() < 1023 {
					stack.dup(1)
				}
			}
		}
	}

	returnStack(stack)
}

// BenchmarkStackBack measures random-depth Back() access.
func BenchmarkStackBack(b *testing.B) {
	stack := newstack()
	for i := 0; i < 256; i++ {
		stack.push(uint256.NewInt(uint64(i)))
	}

	for b.Loop() {
		_ = stack.Back(0)
		_ = stack.Back(15)
		_ = stack.Back(127)
		_ = stack.Back(255)
	}

	returnStack(stack)
}

// BenchmarkStackPoolGetReturn measures allocation/deallocation overhead.
func BenchmarkStackPoolGetReturn(b *testing.B) {
	for b.Loop() {
		s := newstack()
		returnStack(s)
	}
}
