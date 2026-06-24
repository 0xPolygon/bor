package nativectf

import (
	"os"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

func TestRecognition(t *testing.T) {
	// the allowlisted hash must equal keccak256 of the real CTF runtime code
	raw, err := os.ReadFile("testdata/ctf_code.hex")
	if err != nil {
		t.Fatalf("read ctf code: %v", err)
	}
	want := crypto.Keccak256Hash(common.FromHex(strings.TrimSpace(string(raw))))
	if want != canonicalCTFCodeHash {
		t.Fatalf("codehash drift: allowlist=%s testdata=%s", canonicalCTFCodeHash.Hex(), want.Hex())
	}
	if !IsCanonicalCTF(canonicalCTFCodeHash) {
		t.Fatal("canonical CTF codehash must be recognized")
	}
	if IsCanonicalCTF(common.HexToHash("0xdeadbeef")) {
		t.Fatal("unrelated codehash must NOT be recognized")
	}
	if GetCollectionIdSelector != [4]byte{0x85, 0x62, 0x96, 0xf7} {
		t.Fatal("wrong selector")
	}
}
