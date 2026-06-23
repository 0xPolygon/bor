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

package tracers

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/params"
)

func TestDirectoryConcurrentRegisterAndLookup(t *testing.T) {
	t.Parallel()

	d := &directory{elems: make(map[string]elem)}
	d.RegisterJSEval(func(string, *Context, json.RawMessage, *params.ChainConfig) (*Tracer, error) {
		return &Tracer{}, nil
	})
	ctor := func(*Context, json.RawMessage, *params.ChainConfig) (*Tracer, error) {
		return &Tracer{}, nil
	}
	d.Register("tracer-0", ctor, false)

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			d.Register(fmt.Sprintf("tracer-%d", i), ctor, i%2 == 0)
		}(i)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("tracer-%d", i%8)
			if _, err := d.New(name, &Context{}, nil, params.TestChainConfig); err != nil {
				t.Errorf("New(%q) returned error: %v", name, err)
			}
			_ = d.IsJS(name)
		}(i)
	}
	wg.Wait()
}

func TestLiveDirectoryConcurrentRegisterAndLookup(t *testing.T) {
	t.Parallel()

	d := &liveDirectory{elems: make(map[string]ctorFunc)}
	ctor := func(json.RawMessage) (*tracing.Hooks, error) {
		return &tracing.Hooks{}, nil
	}
	d.Register("live-0", ctor)

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			d.Register(fmt.Sprintf("live-%d", i), ctor)
		}(i)
		go func(i int) {
			defer wg.Done()
			_, _ = d.New(fmt.Sprintf("live-%d", i%8), nil)
		}(i)
	}
	wg.Wait()
}
