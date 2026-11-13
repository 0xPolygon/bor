package eth

import (
	"bytes"
	"runtime"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/eth/protocols/wit"
)

// rewritingWitnessPeer answers like fakeWitnessPeer, then lets rewrite alter
// the entry for one page before it is delivered.
type rewritingWitnessPeer struct {
	*fakeWitnessPeer
	atPage  uint64
	rewrite func(*wit.WitnessPageResponse)
}

func (f *rewritingWitnessPeer) RequestWitness(reqs []wit.WitnessPageRequest, sink chan *wit.Response) (*wit.Request, error) {
	page := reqs[0].Page
	f.requested = append(f.requested, page)
	entry := wit.WitnessPageResponse{Data: f.pages[page], Hash: reqs[0].Hash, Page: page, TotalPages: f.totalPages}
	if page == f.atPage && f.rewrite != nil {
		f.rewrite(&entry)
	}
	sink <- &wit.Response{Res: &wit.WitnessPacketRLPPacket{WitnessPacketResponse: wit.WitnessPacketResponse{entry}}}
	return &wit.Request{}, nil
}

func TestFetchWitnessPagesChecksEntriesAgainstRequest(t *testing.T) {
	hash := common.HexToHash("0x51")
	full := bytes.Repeat([]byte{0xab}, PageSize)
	tail := []byte("tail")

	tests := []struct {
		name    string
		pages   map[uint64][]byte
		total   uint64
		atPage  uint64
		rewrite func(*wit.WitnessPageResponse)
		wantOK  bool
	}{
		{"honest multi-page", map[uint64][]byte{0: full, 1: full, 2: tail}, 3, 0, nil, true},
		{"single page over page size", map[uint64][]byte{0: append(full, 0x01)}, 1, 0, nil, false},
		{"later page over page size", map[uint64][]byte{0: full, 1: append(full, 0x01)}, 2, 1, nil, false},
		{"first page for another hash", map[uint64][]byte{0: tail}, 1, 0,
			func(e *wit.WitnessPageResponse) { e.Hash = common.HexToHash("0x52") }, false},
		{"later page for another hash", map[uint64][]byte{0: full, 1: tail}, 2, 1,
			func(e *wit.WitnessPageResponse) { e.Hash = common.HexToHash("0x52") }, false},
		{"later page answered with another index", map[uint64][]byte{0: full, 1: tail}, 2, 1,
			func(e *wit.WitnessPageResponse) { e.Page = 0 }, false},
		{"first page answered with another index", map[uint64][]byte{0: tail}, 1, 0,
			func(e *wit.WitnessPageResponse) { e.Page = 1 }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			peer := &rewritingWitnessPeer{
				fakeWitnessPeer: &fakeWitnessPeer{pages: tt.pages, totalPages: tt.total},
				atPage:          tt.atPage,
				rewrite:         tt.rewrite,
			}
			got, ok := fetchWitnessPages(peer, hash)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			var want []byte
			for p := uint64(0); p < tt.total; p++ {
				want = append(want, tt.pages[p]...)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("got %d bytes, want %d", len(got), len(want))
			}
		})
	}
}

func TestFetchWitnessPagesDoesNotSizeFromClaimedPageCount(t *testing.T) {
	// Page 0 is full and claims the maximum page count, then page 1 is missing:
	// what is held must track the one page that arrived, not the claim.
	peer := &fakeWitnessPeer{
		pages:      map[uint64][]byte{0: bytes.Repeat([]byte{0xcd}, PageSize)},
		totalPages: maxRelayFetchPages,
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, ok := fetchWitnessPages(peer, common.HexToHash("0x53"))
	runtime.ReadMemStats(&after)

	if ok {
		t.Fatal("expected failure when a later page is missing")
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 3*PageSize {
		t.Fatalf("allocated %d bytes for one %d-byte page", allocated, PageSize)
	}
}
