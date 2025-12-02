//go:build !libsecp256k1_sdk
// +build !libsecp256k1_sdk

package secp256k1

import (
	ethCrypto "github.com/ethereum/go-ethereum/crypto"
)

// SigSize is the size of the ECDSA signature.
const SigSize = 65

// Sign creates an ECDSA signature on curve Secp256k1, using SHA256 on the msg.
// The returned signature will be of the form R || S (in lower-S form).
func (privKey *PrivKey) Sign(msg []byte) ([]byte, error) {
	privateObject, err := ethCrypto.ToECDSA(privKey.Key)
	if err != nil {
		return nil, err
	}

	return ethCrypto.Sign(ethCrypto.Keccak256(msg), privateObject)
}

// VerifySignature verifies a signature of the form R || S || V.
// It rejects signatures which are not in lower-S form.
func (pubKey *PubKey) VerifySignature(msg, sigStr []byte) bool {
	if len(sigStr) != SigSize {
		return false
	}

	hash := ethCrypto.Keccak256(msg)
	return ethCrypto.VerifySignature(pubKey.Key, hash, sigStr[:64])
}
