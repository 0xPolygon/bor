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

package build

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
)

func TestPGPSignFile(t *testing.T) {
	entity, privateKey := newArmoredPrivateKey(t)
	keyID, err := PGPKeyID(privateKey)
	if err != nil {
		t.Fatalf("failed to read key ID: %v", err)
	}
	if want := entity.PrimaryKey.KeyIdString(); keyID != want {
		t.Fatalf("key ID mismatch: have %s, want %s", keyID, want)
	}

	message := []byte("bor release artifact")
	input := filepath.Join(t.TempDir(), "artifact")
	signature := input + ".asc"
	if err := os.WriteFile(input, message, 0o600); err != nil {
		t.Fatalf("failed to write input: %v", err)
	}
	if err := PGPSignFile(input, signature, privateKey); err != nil {
		t.Fatalf("failed to sign input: %v", err)
	}
	signatureData, err := os.ReadFile(signature)
	if err != nil {
		t.Fatalf("failed to read signature: %v", err)
	}
	signer, err := openpgp.CheckArmoredDetachedSignature(
		openpgp.EntityList{entity}, bytes.NewReader(message), bytes.NewReader(signatureData), nil,
	)
	if err != nil {
		t.Fatalf("failed to verify signature: %v", err)
	}
	if signer.PrimaryKey.KeyId != entity.PrimaryKey.KeyId {
		t.Fatalf("signer mismatch: have %X, want %X", signer.PrimaryKey.KeyId, entity.PrimaryKey.KeyId)
	}
}

func newArmoredPrivateKey(t *testing.T) (*openpgp.Entity, string) {
	t.Helper()

	entity, err := openpgp.NewEntity("Bor", "release test", "release@polygon.technology", nil)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	var privateKey bytes.Buffer
	armorWriter, err := armor.Encode(&privateKey, openpgp.PrivateKeyType, nil)
	if err != nil {
		t.Fatalf("failed to create armored key: %v", err)
	}
	if err := entity.SerializePrivate(armorWriter, nil); err != nil {
		if closeErr := armorWriter.Close(); closeErr != nil {
			t.Fatalf("failed to serialize private key: %v; failed to close armored key: %v", err, closeErr)
		}
		t.Fatalf("failed to serialize private key: %v", err)
	}
	if err := armorWriter.Close(); err != nil {
		t.Fatalf("failed to close armored key: %v", err)
	}
	return entity, privateKey.String()
}
