// Copyright 2014 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package vm

import (
	"sync"

	"github.com/holiman/uint256"
)

var stackPool = sync.Pool{
	New: func() interface{} {
		return &Stack{}
	},
}

// Stack is an object for basic stack operations. Items popped to the stack are
// expected to be changed and modified. stack does not take care of adding newly
// initialized objects.
//
// Uses a fixed-size [1024]uint256.Int array with a top counter instead of a
// slice, eliminating append/slice-header overhead on push/pop. Ported from GEVM.
//
// top must be first — shares a cache line with data[0], avoiding an extra
// fetch 32 KiB away on every push/pop.
type Stack struct {
	top  int
	data [1024]uint256.Int
}

func newstack() *Stack {
	return stackPool.Get().(*Stack)
}

func returnStack(s *Stack) {
	s.top = 0
	stackPool.Put(s)
}

// Data returns the underlying uint256.Int array.
func (st *Stack) Data() []uint256.Int {
	return st.data[:st.top]
}

func (st *Stack) push(d *uint256.Int) {
	// NOTE: push limit (1024) is checked in baseCheck
	st.data[st.top] = *d
	st.top++
}

func (st *Stack) pop() (ret uint256.Int) {
	st.top--
	ret = st.data[st.top]
	return
}

func (st *Stack) len() int {
	return st.top
}

func (st *Stack) swap1() {
	st.data[st.top-2], st.data[st.top-1] = st.data[st.top-1], st.data[st.top-2]
}
func (st *Stack) swap2() {
	st.data[st.top-3], st.data[st.top-1] = st.data[st.top-1], st.data[st.top-3]
}
func (st *Stack) swap3() {
	st.data[st.top-4], st.data[st.top-1] = st.data[st.top-1], st.data[st.top-4]
}
func (st *Stack) swap4() {
	st.data[st.top-5], st.data[st.top-1] = st.data[st.top-1], st.data[st.top-5]
}
func (st *Stack) swap5() {
	st.data[st.top-6], st.data[st.top-1] = st.data[st.top-1], st.data[st.top-6]
}
func (st *Stack) swap6() {
	st.data[st.top-7], st.data[st.top-1] = st.data[st.top-1], st.data[st.top-7]
}
func (st *Stack) swap7() {
	st.data[st.top-8], st.data[st.top-1] = st.data[st.top-1], st.data[st.top-8]
}
func (st *Stack) swap8() {
	st.data[st.top-9], st.data[st.top-1] = st.data[st.top-1], st.data[st.top-9]
}
func (st *Stack) swap9() {
	st.data[st.top-10], st.data[st.top-1] = st.data[st.top-1], st.data[st.top-10]
}
func (st *Stack) swap10() {
	st.data[st.top-11], st.data[st.top-1] = st.data[st.top-1], st.data[st.top-11]
}
func (st *Stack) swap11() {
	st.data[st.top-12], st.data[st.top-1] = st.data[st.top-1], st.data[st.top-12]
}
func (st *Stack) swap12() {
	st.data[st.top-13], st.data[st.top-1] = st.data[st.top-1], st.data[st.top-13]
}
func (st *Stack) swap13() {
	st.data[st.top-14], st.data[st.top-1] = st.data[st.top-1], st.data[st.top-14]
}
func (st *Stack) swap14() {
	st.data[st.top-15], st.data[st.top-1] = st.data[st.top-1], st.data[st.top-15]
}
func (st *Stack) swap15() {
	st.data[st.top-16], st.data[st.top-1] = st.data[st.top-1], st.data[st.top-16]
}
func (st *Stack) swap16() {
	st.data[st.top-17], st.data[st.top-1] = st.data[st.top-1], st.data[st.top-17]
}

func (st *Stack) dup(n int) {
	st.data[st.top] = st.data[st.top-n]
	st.top++
}

func (st *Stack) peek() *uint256.Int {
	return &st.data[st.top-1]
}

// Back returns the n'th item in stack
func (st *Stack) Back(n int) *uint256.Int {
	return &st.data[st.top-n-1]
}
