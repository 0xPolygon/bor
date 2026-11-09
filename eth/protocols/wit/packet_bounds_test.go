package wit

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/stretchr/testify/require"
)

type decoder interface{ DecodeRLP(*rlp.Stream) error }

func boundsTestMsg(t *testing.T, code uint64, val interface{}) p2p.Msg {
	t.Helper()
	enc, err := rlp.EncodeToBytes(val)
	require.NoError(t, err)
	require.LessOrEqual(t, len(enc), maxMessageSize)
	return p2p.Msg{Code: code, Size: uint32(len(enc)), Payload: bytes.NewReader(enc)}
}

func boundsTestPages(n int) []WitnessPageResponse {
	pages := make([]WitnessPageResponse, n)
	for i := range pages {
		pages[i] = WitnessPageResponse{Data: []byte{0x01}, Hash: common.Hash{byte(i)}, Page: 0, TotalPages: 1}
	}
	return pages
}

func boundsTestMetadata(n int) []WitnessMetadataResponse {
	metadata := make([]WitnessMetadataResponse, n)
	for i := range metadata {
		metadata[i] = WitnessMetadataResponse{Hash: common.Hash{byte(i)}, TotalPages: 1, Available: true}
	}
	return metadata
}

func boundsTestAnnouncement(n int) *NewWitnessHashesPacket {
	packet := &NewWitnessHashesPacket{Hashes: make([]common.Hash, n), Numbers: make([]uint64, n)}
	for i := range packet.Hashes {
		packet.Hashes[i] = common.Hash{byte(i), byte(i >> 8)}
		packet.Numbers[i] = uint64(i)
	}
	return packet
}

func boundsTestWitnessRLP(t *testing.T, state [][]byte) []byte {
	t.Helper()
	enc, err := rlp.EncodeToBytes(&stateless.BorWitness{
		Context: &types.Header{Number: big.NewInt(100)},
		Headers: []*types.Header{{Number: big.NewInt(99)}},
		State:   state,
	})
	require.NoError(t, err)
	return enc
}

func boundsTestPeer(t *testing.T) *Peer {
	t.Helper()
	app, net := p2p.MsgPipe()
	t.Cleanup(func() { app.Close() })
	go func() {
		for {
			msg, err := app.ReadMsg()
			if err != nil {
				return
			}
			msg.Discard()
		}
	}()
	peer := NewPeer(WIT2, p2p.NewPeer(enode.ID{0x42}, "bounds", nil), net, log.New())
	t.Cleanup(peer.Close)
	return peer
}

func TestHandleWitnessMessagesEnforceCounts(t *testing.T) {
	// 32-byte nodes filling a full wit message, the densest honest witness.
	honestState := make([][]byte, (maxMessageSize-1024*1024)/33)
	for i := range honestState {
		honestState[i] = make([]byte, 32)
		big.NewInt(int64(i + 1)).FillBytes(honestState[i])
	}
	honestWitness := rlp.RawValue(boundsTestWitnessRLP(t, honestState))
	emptyNodesWitness := rlp.RawValue(boundsTestWitnessRLP(t, make([][]byte, 1<<20)))

	tests := []struct {
		name        string
		handler     msgHandler
		msg         p2p.Msg
		wantErr     bool
		wantHandled bool
	}{
		{"response at page cap", handleWitness,
			boundsTestMsg(t, MsgWitness, &WitnessPacketRLPPacket{RequestId: 1, WitnessPacketResponse: boundsTestPages(MaxWitnessServe)}), false, false},
		{"response over page cap", handleWitness,
			boundsTestMsg(t, MsgWitness, &WitnessPacketRLPPacket{RequestId: 1, WitnessPacketResponse: boundsTestPages(MaxWitnessServe + 1)}), true, false},
		{"metadata at cap", handleWitnessMetadata,
			boundsTestMsg(t, WitnessMetadataMsg, &WitnessMetadataPacket{RequestId: 1, Metadata: boundsTestMetadata(MaxWitnessMetadataServe)}), false, false},
		{"metadata over cap", handleWitnessMetadata,
			boundsTestMsg(t, WitnessMetadataMsg, &WitnessMetadataPacket{RequestId: 1, Metadata: boundsTestMetadata(MaxWitnessMetadataServe + 1)}), true, false},
		{"announcement at cap", handleNewWitnessHashes,
			boundsTestMsg(t, NewWitnessHashesMsg, boundsTestAnnouncement(MaxWitnessHashesPerPacket)), false, true},
		{"announcement over cap", handleNewWitnessHashes,
			boundsTestMsg(t, NewWitnessHashesMsg, boundsTestAnnouncement(MaxWitnessHashesPerPacket+1)), true, false},
		{"full-size honest witness", handleNewWitness,
			boundsTestMsg(t, NewWitnessMsg, []interface{}{honestWitness}), false, true},
		{"witness of empty nodes", handleNewWitness,
			boundsTestMsg(t, NewWitnessMsg, []interface{}{emptyNodesWitness}), true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handled := false
			backend := &mockBackend{handleFunc: func(*Peer, Packet) error {
				handled = true
				return nil
			}}
			err := tt.handler(backend, tt.msg, boundsTestPeer(t))
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.wantHandled, handled)
		})
	}
}

func TestCappedPacketDecodeRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		in   interface{}
		out  interface{}
	}{
		{"witness response", &WitnessPacketRLPPacket{RequestId: 7, WitnessPacketResponse: boundsTestPages(3)}, new(WitnessPacketRLPPacket)},
		{"witness metadata", &WitnessMetadataPacket{RequestId: 7, Metadata: boundsTestMetadata(3)}, new(WitnessMetadataPacket)},
		{"witness announcement", boundsTestAnnouncement(3), new(NewWitnessHashesPacket)},
		{"empty witness response", &WitnessPacketRLPPacket{RequestId: 7, WitnessPacketResponse: []WitnessPageResponse{}}, new(WitnessPacketRLPPacket)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enc, err := rlp.EncodeToBytes(tt.in)
			require.NoError(t, err)
			require.NoError(t, rlp.DecodeBytes(enc, tt.out))
			require.Equal(t, tt.in, tt.out)
		})
	}
}

func TestCappedPacketDecodeRejectsMalformed(t *testing.T) {
	hash := make([]byte, common.HashLength)
	pages := make([]interface{}, MaxWitnessServe+1)
	for i := range pages {
		pages[i] = []interface{}{[]byte{1}, hash, uint64(0), uint64(1)}
	}
	metadata := make([]interface{}, MaxWitnessMetadataServe+1)
	for i := range metadata {
		metadata[i] = []interface{}{hash, uint64(1), uint64(1), uint64(1), true}
	}
	hashes := make([]interface{}, MaxWitnessHashesPerPacket+1)
	for i := range hashes {
		hashes[i] = hash
	}

	type testCase struct {
		name    string
		out     func() decoder
		in      interface{}
		wantErr string
	}
	responseCases := []struct {
		name    string
		in      interface{}
		wantErr string
	}{
		{"not a list", []byte{1, 2}, rlp.ErrExpectedList.Error()},
		{"request id is a list", []interface{}{[]interface{}{}, []interface{}{}}, "for uint64"},
		{"items not a list", []interface{}{uint64(1), []byte{1}}, rlp.ErrExpectedList.Error()},
		{"bad item", []interface{}{uint64(1), []interface{}{[]interface{}{}}}, "too few elements"},
		{"trailing field", []interface{}{uint64(1), []interface{}{}, uint64(2)}, "not positioned at EOL"},
	}
	tests := []testCase{
		{"response/over cap", func() decoder { return new(WitnessPacketRLPPacket) }, []interface{}{uint64(1), pages}, "limit"},
		{"metadata/over cap", func() decoder { return new(WitnessMetadataPacket) }, []interface{}{uint64(1), metadata}, "limit"},
		{"announcement/not a list", func() decoder { return new(NewWitnessHashesPacket) }, []byte{1, 2}, rlp.ErrExpectedList.Error()},
		{"announcement/hashes not a list", func() decoder { return new(NewWitnessHashesPacket) }, []interface{}{[]byte{1}, []interface{}{}}, rlp.ErrExpectedList.Error()},
		{"announcement/numbers not a list", func() decoder { return new(NewWitnessHashesPacket) }, []interface{}{[]interface{}{hash}, []byte{1}}, rlp.ErrExpectedList.Error()},
		{"announcement/trailing field", func() decoder { return new(NewWitnessHashesPacket) }, []interface{}{[]interface{}{hash}, []interface{}{uint64(1)}, uint64(2)}, "not positioned at EOL"},
		{"announcement/hashes over cap", func() decoder { return new(NewWitnessHashesPacket) }, []interface{}{hashes, []interface{}{}}, "limit"},
	}
	for _, c := range responseCases {
		tests = append(tests,
			testCase{"response/" + c.name, func() decoder { return new(WitnessPacketRLPPacket) }, c.in, c.wantErr},
			testCase{"metadata/" + c.name, func() decoder { return new(WitnessMetadataPacket) }, c.in, c.wantErr},
		)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enc, err := rlp.EncodeToBytes(tt.in)
			require.NoError(t, err)
			err = tt.out().DecodeRLP(rlp.NewStream(bytes.NewReader(enc), 0))
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func boundsTestSigned(n, sigLen int) *SignedNewWitnessHashesPacket {
	packet := &SignedNewWitnessHashesPacket{Announcements: make([]SignedWitnessAnnouncement, n)}
	for i := range packet.Announcements {
		packet.Announcements[i] = SignedWitnessAnnouncement{
			BlockHash:   common.Hash{byte(i), byte(i >> 8)},
			BlockNumber: uint64(i),
			WitnessHash: common.Hash{0xff, byte(i)},
			WitnessSize: uint64(i + 1),
			Signature:   bytes.Repeat([]byte{byte(i) | 1}, sigLen),
		}
	}
	return packet
}

func TestHandleSignedNewWitnessHashesEnforcesBounds(t *testing.T) {
	tests := []struct {
		name        string
		packet      *SignedNewWitnessHashesPacket
		wantErr     string
		wantHandled bool
	}{
		{"single announcement", boundsTestSigned(1, SignatureLength), "", true},
		{"announcements at cap", boundsTestSigned(MaxSignedAnnouncesPerPacket, SignatureLength), "", true},
		{"announcements over cap", boundsTestSigned(MaxSignedAnnouncesPerPacket+1, SignatureLength), "exceed 64 item limit", false},
		{"no announcements", boundsTestSigned(0, SignatureLength), "cannot be empty", false},
		{"short signature left to verification", boundsTestSigned(1, SignatureLength-1), "", true},
		{"oversized signature", boundsTestSigned(1, SignatureLength+1), "signature exceeds", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handled := false
			backend := &mockBackend{handleFunc: func(*Peer, Packet) error {
				handled = true
				return nil
			}}
			err := handleSignedNewWitnessHashes(backend, boundsTestMsg(t, SignedNewWitnessHashesMsg, tt.packet), boundsTestPeer(t))
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.wantHandled, handled)
		})
	}
}

// reflectSignedAnnouncement mirrors SignedWitnessAnnouncement without its
// DecodeRLP, so the reflection decoding is the reference for the wire format.
type reflectSignedAnnouncement struct {
	BlockHash   common.Hash
	BlockNumber uint64
	WitnessHash common.Hash
	WitnessSize uint64
	Signature   []byte
}

func TestSignedNewWitnessHashesDecodeMatchesReflection(t *testing.T) {
	for _, packet := range []*SignedNewWitnessHashesPacket{
		boundsTestSigned(3, SignatureLength),
		boundsTestSigned(1, 0),
		boundsTestSigned(1, 1),
	} {
		enc, err := rlp.EncodeToBytes(packet)
		require.NoError(t, err)

		var reference struct{ Announcements []reflectSignedAnnouncement }
		require.NoError(t, rlp.DecodeBytes(enc, &reference))
		got := new(SignedNewWitnessHashesPacket)
		require.NoError(t, rlp.DecodeBytes(enc, got))

		require.Len(t, got.Announcements, len(reference.Announcements))
		for i, ref := range reference.Announcements {
			require.Equal(t, ref, reflectSignedAnnouncement(got.Announcements[i]))
		}
		reenc, err := rlp.EncodeToBytes(got)
		require.NoError(t, err)
		require.Equal(t, enc, reenc)
	}
}

func TestSignedWitnessAnnouncementDecodeRejectsMalformed(t *testing.T) {
	hash := make([]byte, common.HashLength)
	sig := make([]byte, SignatureLength)
	list := []interface{}{}
	announcement := func() decoder { return new(SignedWitnessAnnouncement) }
	packet := func() decoder { return new(SignedNewWitnessHashesPacket) }
	tests := []struct {
		name    string
		out     func() decoder
		in      interface{}
		wantErr string
	}{
		{"not a list", announcement, []byte{1, 2}, rlp.ErrExpectedList.Error()},
		{"block hash is a list", announcement, []interface{}{list, uint64(1), hash, uint64(1), sig}, "common.Hash"},
		{"block number is a list", announcement, []interface{}{hash, list, hash, uint64(1), sig}, "uint64"},
		{"witness hash is a list", announcement, []interface{}{hash, uint64(1), list, uint64(1), sig}, "common.Hash"},
		{"witness size is a list", announcement, []interface{}{hash, uint64(1), hash, list, sig}, "uint64"},
		{"missing signature", announcement, []interface{}{hash, uint64(1), hash, uint64(1)}, rlp.EOL.Error()},
		{"signature is a list", announcement, []interface{}{hash, uint64(1), hash, uint64(1), list}, rlp.ErrExpectedString.Error()},
		{"signature over length", announcement, []interface{}{hash, uint64(1), hash, uint64(1), make([]byte, SignatureLength+1)}, "signature exceeds"},
		{"trailing field", announcement, []interface{}{hash, uint64(1), hash, uint64(1), sig, uint64(2)}, "not positioned at EOL"},
		{"packet not a list", packet, []byte{1, 2}, rlp.ErrExpectedList.Error()},
		{"announcements not a list", packet, []interface{}{[]byte{1}}, rlp.ErrExpectedList.Error()},
		{"packet trailing field", packet, []interface{}{list, uint64(2)}, "not positioned at EOL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enc, err := rlp.EncodeToBytes(tt.in)
			require.NoError(t, err)
			err = tt.out().DecodeRLP(rlp.NewStream(bytes.NewReader(enc), 0))
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}
