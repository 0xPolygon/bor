// Copyright 2020 The go-ethereum Authors
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

package p2p

import (
	"bytes"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/davecgh/go-spew/spew"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/p2p/pipes"
)

func TestProtocolHandshake(t *testing.T) {
	var (
		prv0, _ = crypto.GenerateKey()
		pub0    = crypto.FromECDSAPub(&prv0.PublicKey)[1:]
		hs0     = &protoHandshake{Version: 3, ID: pub0, Caps: []Cap{{"a", 0}, {"b", 2}}}

		prv1, _ = crypto.GenerateKey()
		pub1    = crypto.FromECDSAPub(&prv1.PublicKey)[1:]
		hs1     = &protoHandshake{Version: 3, ID: pub1, Caps: []Cap{{"c", 1}, {"d", 3}}}

		wg sync.WaitGroup
	)

	fd0, fd1, err := pipes.TCPPipe()
	if err != nil {
		t.Fatal(err)
	}

	wg.Add(2)

	go func() {
		defer wg.Done()
		defer fd0.Close()
		frame := newRLPX(fd0, &prv1.PublicKey)

		rpubkey, err := frame.doEncHandshake(prv0)
		if err != nil {
			t.Errorf("dial side enc handshake failed: %v", err)
			return
		}

		if !reflect.DeepEqual(rpubkey, &prv1.PublicKey) {
			t.Errorf("dial side remote pubkey mismatch: got %v, want %v", rpubkey, &prv1.PublicKey)
			return
		}

		phs, err := frame.doProtoHandshake(hs0)
		if err != nil {
			t.Errorf("dial side proto handshake error: %v", err)
			return
		}

		phs.Rest = nil
		if !reflect.DeepEqual(phs, hs1) {
			t.Errorf("dial side proto handshake mismatch:\ngot: %s\nwant: %s\n", spew.Sdump(phs), spew.Sdump(hs1))
			return
		}

		frame.close(DiscQuitting)
	}()
	go func() {
		defer wg.Done()
		defer fd1.Close()
		rlpx := newRLPX(fd1, nil)

		rpubkey, err := rlpx.doEncHandshake(prv1)
		if err != nil {
			t.Errorf("listen side enc handshake failed: %v", err)
			return
		}

		if !reflect.DeepEqual(rpubkey, &prv0.PublicKey) {
			t.Errorf("listen side remote pubkey mismatch: got %v, want %v", rpubkey, &prv0.PublicKey)
			return
		}

		phs, err := rlpx.doProtoHandshake(hs1)
		if err != nil {
			t.Errorf("listen side proto handshake error: %v", err)
			return
		}

		phs.Rest = nil
		if !reflect.DeepEqual(phs, hs0) {
			t.Errorf("listen side proto handshake mismatch:\ngot: %s\nwant: %s\n", spew.Sdump(phs), spew.Sdump(hs0))
			return
		}

		if err := ExpectMsg(rlpx, discMsg, []any{DiscQuitting}); err != nil {
			t.Errorf("error receiving disconnect: %v", err)
		}
	}()
	wg.Wait()
}

func TestProtocolHandshakeErrors(t *testing.T) {
	tests := []struct {
		code uint64
		msg  interface{}
		err  error
	}{
		{
			code: discMsg,
			msg:  []any{DiscQuitting},
			err:  DiscQuitting,
		},
		{
			// legacy disconnect encoding as byte array
			code: discMsg,
			msg:  []byte{byte(DiscQuitting)},
			err:  DiscQuitting,
		},
		{
			code: 0x989898,
			msg:  []byte{1},
			err:  errors.New("expected handshake, got 989898"),
		},
		{
			code: handshakeMsg,
			msg:  make([]byte, baseProtocolMaxMsgSize+2),
			err:  errors.New("message too big"),
		},
		{
			code: handshakeMsg,
			msg:  []byte{1, 2, 3},
			err:  newPeerError(errInvalidMsg, "(code 0) (size 4) rlp: expected input list for p2p.protoHandshake"),
		},
		{
			code: handshakeMsg,
			msg:  &protoHandshake{Version: 3},
			err:  DiscInvalidIdentity,
		},
	}

	for i, test := range tests {
		p1, p2 := MsgPipe()
		go Send(p1, test.code, test.msg)

		_, err := readProtocolHandshake(p2)
		if !reflect.DeepEqual(err, test.err) {
			t.Errorf("test %d: error mismatch: got %q, want %q", i, err, test.err)
		}
	}
}

// encHandshakePair returns two transports connected over TCP that completed the encryption
// handshake, so the protocol handshake is next.
func encHandshakePair(t *testing.T) (dialer, listener *rlpxTransport) {
	t.Helper()

	var (
		prv0, _ = crypto.GenerateKey()
		prv1, _ = crypto.GenerateKey()
		wg      sync.WaitGroup
	)

	fd0, fd1, err := pipes.TCPPipe()
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { fd0.Close(); fd1.Close() })

	dialer, listener = newRLPX(fd0, &prv1.PublicKey).(*rlpxTransport), newRLPX(fd1, nil).(*rlpxTransport)

	wg.Add(2)

	go func() {
		defer wg.Done()

		if _, err := dialer.doEncHandshake(prv0); err != nil {
			t.Errorf("dial side enc handshake failed: %v", err)
		}
	}()
	go func() {
		defer wg.Done()

		if _, err := listener.doEncHandshake(prv1); err != nil {
			t.Errorf("listen side enc handshake failed: %v", err)
		}
	}()
	wg.Wait()

	return dialer, listener
}

// This test checks the frame size limit that applies while the protocol handshake is read.
func TestProtocolHandshakeFrameLimit(t *testing.T) {
	const frameTooLarge = "frame size exceeds limit"

	// The frame holds the message code (one byte for the handshake) and the payload.
	maxFrame := baseProtocolMaxMsgSize + 9

	for _, test := range []struct {
		name    string
		payload int
		want    string // the error, empty if only known not to be the frame limit
	}{
		{"base message size", baseProtocolMaxMsgSize, ""},
		{"largest frame", maxFrame - 1, "message too big"},
		{"frame above the limit", maxFrame, frameTooLarge},
		{"far above the limit", baseProtocolMaxMsgSize * 2, frameTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			dialer, listener := encHandshakePair(t)

			go dialer.conn.Write(handshakeMsg, make([]byte, test.payload))

			// The frames within the limit are rejected later, as the payload is not a valid
			// handshake or exceeds the message size limit.
			_, err := listener.doProtoHandshake(&protoHandshake{Version: 3})
			if err == nil {
				t.Fatal("handshake succeeded")
			}

			if test.want == "" && err.Error() == frameTooLarge || test.want != "" && err.Error() != test.want {
				t.Fatalf("error mismatch: have %q, want %q", err, test.want)
			}
		})
	}
}

// This test checks that the frame size limit is lifted once the protocol handshake is done.
func TestProtocolHandshakeLiftsFrameLimit(t *testing.T) {
	var wg sync.WaitGroup

	// Registered first, so it runs after the connections are closed.
	t.Cleanup(wg.Wait)

	dialer, listener := encHandshakePair(t)

	wg.Add(1)

	go func() {
		defer wg.Done()

		if _, err := dialer.doProtoHandshake(&protoHandshake{Version: 3, ID: bytes.Repeat([]byte{1}, 64)}); err != nil {
			t.Errorf("dial side proto handshake error: %v", err)
			return
		}

		if err := Send(dialer, 0x10, make([]byte, 4*baseProtocolMaxMsgSize)); err != nil {
			t.Errorf("dial side send error: %v", err)
		}
	}()

	if _, err := listener.doProtoHandshake(&protoHandshake{Version: 3, ID: bytes.Repeat([]byte{2}, 64)}); err != nil {
		t.Fatalf("listen side proto handshake error: %v", err)
	}

	msg, err := listener.ReadMsg()
	if err != nil {
		t.Fatalf("large message after the handshake rejected: %v", err)
	}

	if msg.Size < 4*baseProtocolMaxMsgSize {
		t.Fatalf("message size mismatch: have %d", msg.Size)
	}
}
