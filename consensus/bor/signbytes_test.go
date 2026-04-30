package bor

import (
	"bytes"
	"testing"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
)

// TestSignBytesForwardsMimetype is the regression for the wit2 announce
// signing path's external-signer compatibility: bor.SignBytes must hand the
// caller-supplied mimetype to the configured signer untouched. Operators
// configuring Clef whitelist a specific string ("application/x-bor-wit2-
// announce"); if SignBytes ever rewrote, lower-cased, or stripped that, the
// signer would either reject the request or sign under a different domain.
//
// The test captures the (mimetype, payload) the wallet sees and asserts both
// match exactly what the caller passed.
func TestSignBytesForwardsMimetype(t *testing.T) {
	bor := &Bor{}
	addr := common.HexToAddress("0x1234")

	var (
		gotMimetype string
		gotPayload  []byte
	)
	bor.Authorize(addr, func(_ accounts.Account, mimetype string, data []byte) ([]byte, error) {
		gotMimetype = mimetype
		gotPayload = append([]byte(nil), data...)
		return make([]byte, 65), nil
	})

	preimage := []byte("wit2-announce-preimage")
	signer, sig, err := bor.SignBytes(accounts.MimetypeBorWitnessAnnounce, preimage)
	if err != nil {
		t.Fatalf("SignBytes: %v", err)
	}
	if signer != addr {
		t.Fatalf("signer addr mismatch: got %s want %s", signer, addr)
	}
	if len(sig) != 65 {
		t.Fatalf("expected 65-byte signature, got %d", len(sig))
	}
	if gotMimetype != accounts.MimetypeBorWitnessAnnounce {
		t.Fatalf("mimetype not forwarded literally: got %q want %q",
			gotMimetype, accounts.MimetypeBorWitnessAnnounce)
	}
	if !bytes.Equal(gotPayload, preimage) {
		t.Fatalf("payload not forwarded literally: got %x want %x", gotPayload, preimage)
	}
}

// TestSignBytesRejectsHeaderMimetype guards against accidental cross-context
// reuse: callers must never pass MimetypeBor (header sealing) into SignBytes,
// since that would let an announce signature replay as a block-seal.
func TestSignBytesRejectsHeaderMimetype(t *testing.T) {
	bor := &Bor{}
	bor.Authorize(common.HexToAddress("0x1234"), func(accounts.Account, string, []byte) ([]byte, error) {
		t.Fatal("signFn must not be reached for rejected mimetype")
		return nil, nil
	})

	if _, _, err := bor.SignBytes("", []byte{0x01}); err == nil {
		t.Fatal("empty mimetype must be rejected")
	}
	if _, _, err := bor.SignBytes(accounts.MimetypeBor, []byte{0x01}); err == nil {
		t.Fatal("MimetypeBor must be rejected to prevent header-seal replay")
	}
}
