// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
// The go-ethereum library is distributed under the GNU Lesser General Public License v3.0.

package snap

import (
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/tracker"
)

type countingWriter struct{ writes int }

func (w *countingWriter) ReadMsg() (p2p.Msg, error) { return p2p.Msg{}, errors.New("not used") }
func (w *countingWriter) WriteMsg(msg p2p.Msg) error {
	w.writes++
	return msg.Discard()
}

func TestRequestStorageRangesUntracked(t *testing.T) {
	rw := new(countingWriter)
	peer := NewFakePeer(SNAP1, "0123456789abcdef", rw)
	peer.tracker.Stop()

	err := peer.RequestStorageRanges(1, common.Hash{}, []common.Hash{{1}}, nil, nil, 1024)
	if !errors.Is(err, tracker.ErrStopped) {
		t.Fatalf("err = %v, want %v", err, tracker.ErrStopped)
	}
	if rw.writes != 0 {
		t.Fatal("untracked request was sent")
	}
}
