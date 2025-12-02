package secp256k1

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"io"
	"math/big"

	secp256k1 "github.com/btcsuite/btcd/btcec/v2"
	ethCrypto "github.com/ethereum/go-ethereum/crypto" //nolint:depguard

	"github.com/cometbft/cometbft/crypto"
	cmtjson "github.com/cometbft/cometbft/libs/json"
)

// -------------------------------------
const (
	PrivKeyNameOld = "tendermint/PrivKeySecp256k1"
	PubKeyNameOld  = "tendermint/PubKeySecp256k1"
	PrivKeyName    = "cometbft/PrivKeySecp256k1eth"
	PubKeyName     = "cometbft/PubKeySecp256k1eth"

	KeyType     = "secp256k1"
	PrivKeySize = 32
)

func init() {
	cmtjson.RegisterType(PubKey{}, PubKeyName)
	cmtjson.RegisterType(PrivKey{}, PrivKeyName)
	cmtjson.RegisterType(PubKeyOld{}, PubKeyNameOld)
	cmtjson.RegisterType(PrivKeyOld{}, PrivKeyNameOld)
}

var _ crypto.PrivKey = PrivKey{}
var _ crypto.PrivKey = PrivKeyOld{}

// PrivKey implements PrivKey.
type PrivKey []byte

type PrivKeyOld []byte

func (privKey PrivKeyOld) Bytes() []byte {
	return PrivKey(privKey).Bytes()
}
func (privKey PrivKeyOld) PubKey() crypto.PubKey {
	return PrivKey(privKey).PubKey()
}
func (privKey PrivKeyOld) Equals(other crypto.PrivKey) bool {
	return PrivKey(privKey).Equals(other)
}
func (privKey PrivKeyOld) Type() string {
	return PrivKey(privKey).Type()
}
func (privKey PrivKeyOld) Sign(msg []byte) ([]byte, error) {
	return PrivKey(privKey).Sign(msg)
}

// Bytes marshals the private key using amino encoding.
func (privKey PrivKey) Bytes() []byte {
	return []byte(privKey)
}

// PubKey performs the point-scalar multiplication from the privKey on the
// generator point to get the pubkey.
func (privKey PrivKey) PubKey() crypto.PubKey {
	privateObject, err := ethCrypto.ToECDSA(privKey)
	if err != nil {
		panic(err)
	}

	pk := ethCrypto.FromECDSAPub(&privateObject.PublicKey)

	return PubKey(pk)
}

// Equals - you probably don't need to use this.
// Runs in constant time based on length of the keys.
func (privKey PrivKey) Equals(other crypto.PrivKey) bool {
	if otherSecp, ok := other.(PrivKey); ok {
		return subtle.ConstantTimeCompare(privKey[:], otherSecp[:]) == 1
	}
	return false
}

func (privKey PrivKey) Type() string {
	return KeyType
}

// GenPrivKey generates a new ECDSA private key on curve secp256k1 private key.
// It uses OS randomness to generate the private key.
func GenPrivKey() PrivKey {
	return genPrivKey(crypto.CReader())
}

// genPrivKey generates a new secp256k1 private key using the provided reader.
func genPrivKey(rand io.Reader) PrivKey {
	var privKeyBytes [PrivKeySize]byte
	d := new(big.Int)

	for {
		privKeyBytes = [PrivKeySize]byte{}
		_, err := io.ReadFull(rand, privKeyBytes[:])
		if err != nil {
			panic(err)
		}

		d.SetBytes(privKeyBytes[:])
		// break if we found a valid point (i.e. > 0 and < N == curveOrder)
		isValidFieldElement := 0 < d.Sign() && d.Cmp(secp256k1.S256().N) < 0
		if isValidFieldElement {
			break
		}
	}

	// crypto.CRandBytes is guaranteed to be 32 bytes long, so it can be
	// cast to PrivKey.
	return PrivKey(privKeyBytes[:])
}

var one = new(big.Int).SetInt64(1)

// GenPrivKeySecp256k1 hashes the secret with SHA2, and uses
// that 32 byte output to create the private key.
//
// It makes sure the private key is a valid field element by setting:
//
// c = sha256(secret)
// k = (c mod (n − 1)) + 1, where n = curve order.
//
// NOTE: secret should be the output of a KDF like bcrypt,
// if it's derived from user input.
func GenPrivKeySecp256k1(secret []byte) PrivKey {
	secHash := sha256.Sum256(secret)
	// to guarantee that we have a valid field element, we use the approach of:
	// "Suite B Implementer’s Guide to FIPS 186-3", A.2.1
	// https://apps.nsa.gov/iaarchive/library/ia-guidance/ia-solutions-for-classified/algorithm-guidance/suite-b-implementers-guide-to-fips-186-3-ecdsa.cfm
	// see also https://github.com/golang/go/blob/0380c9ad38843d523d9c9804fe300cb7edd7cd3c/src/crypto/ecdsa/ecdsa.go#L89-L101
	fe := new(big.Int).SetBytes(secHash[:])
	n := new(big.Int).Sub(secp256k1.S256().N, one)
	fe.Mod(fe, n)
	fe.Add(fe, one)

	feB := fe.Bytes()
	privKey32 := make([]byte, PrivKeySize)
	// copy feB over to fixed 32 byte privKey32 and pad (if necessary)
	copy(privKey32[32-len(feB):32], feB)

	return PrivKey(privKey32)
}

// Sign creates an ECDSA signature on curve Secp256k1, using SHA256 on the msg.
// The returned signature will be of the form R || S || V (in lower-S form).
func (privKey PrivKey) Sign(msg []byte) ([]byte, error) {
	privateObject, err := ethCrypto.ToECDSA(privKey)
	if err != nil {
		return nil, err
	}

	return ethCrypto.Sign(ethCrypto.Keccak256(msg), privateObject)
}

//-------------------------------------

var _ crypto.PubKey = PubKey{}
var _ crypto.PubKey = PubKeyOld{}

// PubKeySize (uncompressed) is composed of 65 bytes for two field elements (x and y)
// and a prefix byte (0x04) to indicate that it is uncompressed.
const PubKeySize = 65

// SigSize is the size of the ECDSA signature.
const SigSize = 65

// PubKey implements crypto.PubKey.
// It is the uncompressed form of the pubkey. The first byte is prefixed with 0x04.
// This prefix is followed with the (x,y)-coordinates.
type PubKey []byte
type PubKeyOld []byte

func (pubKey PubKeyOld) Address() crypto.Address {
	return PubKey(pubKey).Address()
}

func (pubKey PubKeyOld) Bytes() []byte {
	return PubKey(pubKey).Bytes()
}

func (pubKey PubKeyOld) String() string {
	return PubKey(pubKey).String()
}

func (pubKey PubKeyOld) Equals(other crypto.PubKey) bool {
	return PubKey(pubKey).Equals(other)
}

func (pubKey PubKeyOld) Type() string {
	return PubKey(pubKey).Type()
}

func (pubKey PubKeyOld) VerifySignature(msg []byte, sigStr []byte) bool {
	return PubKey(pubKey).VerifySignature(msg, sigStr)
}

// Address returns a Ethereym style addresses: Last_20_Bytes(KECCAK256(pubkey))
func (pubKey PubKey) Address() crypto.Address {
	if len(pubKey) != PubKeySize {
		panic(fmt.Sprintf("length of pubkey is incorrect %d != %d", len(pubKey), PubKeySize))
	}
	return ethCrypto.Keccak256(pubKey[1:])[12:]
}

// Bytes returns the pubkey marshaled with amino encoding.
func (pubKey PubKey) Bytes() []byte {
	return []byte(pubKey)
}

func (pubKey PubKey) String() string {
	return fmt.Sprintf("PubKeySecp256k1{%X}", []byte(pubKey))
}

func (pubKey PubKey) Equals(other crypto.PubKey) bool {
	if otherSecp, ok := other.(PubKey); ok {
		return bytes.Equal(pubKey[:], otherSecp[:])
	}
	return false
}

func (pubKey PubKey) Type() string {
	return KeyType
}

// VerifySignature verifies a signature of the form R || S || V.
// It rejects signatures which are not in lower-S form.
func (pubKey PubKey) VerifySignature(msg []byte, sigStr []byte) bool {
	if len(sigStr) != SigSize {

		return false
	}

	hash := ethCrypto.Keccak256(msg)
	return ethCrypto.VerifySignature(pubKey, hash, sigStr[:64])
}
