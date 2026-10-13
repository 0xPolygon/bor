// Copyright 2026 The go-ethereum Authors
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

package readattr

import (
	"sync"
	"testing"
	"time"
)

func TestTagSetTakeReset(t *testing.T) {
	SetEnabled(true)
	defer func() { SetEnabled(false) }()

	Set("tx:1:0xabc")
	if got := Current(); got != "tx:1:0xabc" {
		t.Fatalf("Current() = %q, want tx:1:0xabc", got)
	}
	Record("account", "deadbeef", 10, time.Millisecond)
	Record("storage", "aa/bb", 1, time.Millisecond)
	if got := Take("tx:1:0xabc"); got != 2 {
		t.Fatalf("Take = %d, want 2", got)
	}
	if got := Take("tx:1:0xabc"); got != 0 {
		t.Fatalf("Take after delete = %d, want 0", got)
	}
	Reset()
	if got := Current(); got != "" {
		t.Fatalf("Current() after Reset = %q, want empty", got)
	}
}

func TestGoroutineIsolation(t *testing.T) {
	SetEnabled(true)
	defer func() { SetEnabled(false) }()

	Set("tx:main")
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		Set("tx:other")
		Record("storage", "aa/bb", 1, 0)
	}()
	wg.Wait()
	if got := Take("tx:other"); got != 1 {
		t.Fatalf("goroutine tag isolated: Take(tx:other) = %d, want 1", got)
	}
	if got := Current(); got != "tx:main" {
		t.Fatalf("main goroutine tag preserved: %q, want tx:main", got)
	}
	Reset()
}

func TestDisabledNoop(t *testing.T) {
	SetEnabled(false)
	Set("tx:x")
	if got := Current(); got != "" {
		t.Fatalf("Current() when disabled = %q, want empty", got)
	}
	Record("account", "k", 1, 0) // must not record
	if got := Take("tx:x"); got != 0 {
		t.Fatalf("Take when disabled = %d, want 0", got)
	}
}

func TestRecent(t *testing.T) {
	SetEnabled(true)
	defer func() { SetEnabled(false) }()

	Reset()
	Set("t:recent")
	for i := 0; i < 5; i++ {
		Record("node", "k", 1, 0)
	}
	Reset()
	if got := len(Recent(3)); got != 3 {
		t.Fatalf("Recent(3) len = %d, want 3", got)
	}
}
