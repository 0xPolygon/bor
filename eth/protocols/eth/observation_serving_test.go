package eth

import (
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
	"github.com/ethereum/go-ethereum/rlp"
)

type observedWriter struct {
	err       error
	completed bool
}

func (w *observedWriter) ReadMsg() (p2p.Msg, error) { return p2p.Msg{}, errors.New("not used") }
func (w *observedWriter) WriteMsg(msg p2p.Msg) error {
	if w.err != nil {
		return w.err
	}
	if err := msg.Discard(); err != nil {
		return err
	}
	w.completed = true
	return nil
}

func TestPeerPolicyCompletedBodyReply(t *testing.T) {
	for _, failure := range []bool{false, true} {
		w := &observedWriter{}
		if failure {
			w.err = errors.New("write failed")
		}
		calls := 0
		p := &Peer{rw: w, observer: func(e peerpolicy.Evidence) {
			calls++
			if !w.completed || e.Family != peerpolicy.BodyReplies {
				t.Fatal("recorded before completion")
			}
		}}
		err := p.writeObservedBodies(1, []rlp.RawValue{{0xc0}}, peerpolicy.Evidence{Family: peerpolicy.BodyReplies})
		if !errors.Is(err, w.err) {
			t.Fatalf("changed write error: %v", err)
		}
		wantCalls := 1
		if failure {
			wantCalls = 0
		}
		if calls != wantCalls {
			t.Fatalf("calls=%d want=%d", calls, wantCalls)
		}
	}
}

func TestPeerPolicyBodyManifestSkipsMissingObjects(t *testing.T) {
	backend := newTestBackend(2)
	defer backend.close()
	hash := backend.chain.GetBlockByNumber(1).Hash()
	query := GetBlockBodiesRequest{{}, hash, {}}
	var recorded []common.Hash
	var bytes int
	response := serviceGetBlockBodiesQuery(backend.chain, query, func(hash common.Hash, size int) {
		recorded = append(recorded, hash)
		bytes += size
	})
	if len(recorded) != 1 || recorded[0] != hash || len(response) != 1 || bytes != len(response[0]) {
		t.Fatalf("manifest did not match response: %v", recorded)
	}
}

func TestPeerPolicyBodyServingAdapter(t *testing.T) {
	backend := newTestBackend(2)
	defer backend.close()
	hash := backend.chain.GetBlockByNumber(1).Hash()
	for _, enabled := range []bool{false, true} {
		w := &observedWriter{}
		p := &Peer{rw: w}
		var got peerpolicy.Evidence
		if enabled {
			p.SetObserver(func(e peerpolicy.Evidence) { got = e })
		}
		query := GetBlockBodiesPacket{RequestId: 7, GetBlockBodiesRequest: GetBlockBodiesRequest{{}, hash}}
		if err := observedBodyReply(backend, p, query); err != nil {
			t.Fatal(err)
		}
		if !w.completed {
			t.Fatal("reply was not written")
		}
		if enabled && (got.Items != 1 || got.Bytes == 0 || len(got.Hashes) != 1 || got.Hashes[0] != hash || got.ObjectBytes[0] != got.Bytes) {
			t.Fatalf("incorrect completed reply observation: %+v", got)
		}
	}
}
