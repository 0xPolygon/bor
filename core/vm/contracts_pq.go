package vm

import (
	"github.com/cloudflare/circl/sign/mldsa/mldsa44"
	"github.com/cloudflare/circl/sign/mldsa/mldsa65"
	"github.com/cloudflare/circl/sign/mldsa/mldsa87"
	"github.com/cloudflare/circl/sign/slhdsa"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/params"
)

// mldsaVerify implements the ML-DSA verification precompiles of EIP-8355 (draft).
//
// Input is pubkey || signature || message, where pubkey and signature use the
// FIPS 204 encodings and the message is everything after them. Verification is
// pure ML-DSA with the empty context string. The output is always 32 bytes:
// 1 for a valid signature, 0 for anything else, including malformed input.
// The call never fails except when it runs out of gas.
//
// These contracts are not activated in any fork yet.
type mldsaVerify struct {
	name    string
	pkLen   int
	sigLen  int
	baseGas uint64
	verify  func(pk, sig, msg []byte) bool
}

var (
	mldsa44Verify = &mldsaVerify{
		name:    "VERIFY_MLDSA44",
		pkLen:   mldsa44.PublicKeySize,
		sigLen:  mldsa44.SignatureSize,
		baseGas: params.MLDSA44VerifyBaseGas,
		verify: func(pk, sig, msg []byte) bool {
			var key mldsa44.PublicKey
			if key.UnmarshalBinary(pk) != nil {
				return false
			}
			return mldsa44.Verify(&key, msg, nil, sig)
		},
	}
	mldsa65Verify = &mldsaVerify{
		name:    "VERIFY_MLDSA65",
		pkLen:   mldsa65.PublicKeySize,
		sigLen:  mldsa65.SignatureSize,
		baseGas: params.MLDSA65VerifyBaseGas,
		verify: func(pk, sig, msg []byte) bool {
			var key mldsa65.PublicKey
			if key.UnmarshalBinary(pk) != nil {
				return false
			}
			return mldsa65.Verify(&key, msg, nil, sig)
		},
	}
	mldsa87Verify = &mldsaVerify{
		name:    "VERIFY_MLDSA87",
		pkLen:   mldsa87.PublicKeySize,
		sigLen:  mldsa87.SignatureSize,
		baseGas: params.MLDSA87VerifyBaseGas,
		verify: func(pk, sig, msg []byte) bool {
			var key mldsa87.PublicKey
			if key.UnmarshalBinary(pk) != nil {
				return false
			}
			return mldsa87.Verify(&key, msg, nil, sig)
		},
	}
)

// RequiredGas returns BASE + 6 gas per 32-byte word of message.
// Input shorter than pubkey + signature has no message and pays only BASE.
func (c *mldsaVerify) RequiredGas(input []byte) uint64 {
	msgLen := len(input) - c.pkLen - c.sigLen
	if msgLen <= 0 {
		return c.baseGas
	}
	return c.baseGas + toWordSize(uint64(msgLen))*params.MLDSAVerifyPerWordGas
}

func (c *mldsaVerify) Run(input []byte) ([]byte, error) {
	if len(input) < c.pkLen+c.sigLen {
		return common.Hash{}.Bytes(), nil
	}
	pk := input[:c.pkLen]
	sig := input[c.pkLen : c.pkLen+c.sigLen]
	msg := input[c.pkLen+c.sigLen:]
	if c.verify(pk, sig, msg) {
		return true32Byte, nil
	}
	return common.Hash{}.Bytes(), nil
}

func (c *mldsaVerify) Name() string {
	return c.name
}

// slhdsaVerify implements SLH-DSA-SHA2-128s (FIPS 205) verification with the
// same interface as the EIP-8355 ML-DSA precompiles: pubkey || signature ||
// message in, a 32-byte 0/1 out, pure SLH-DSA with the empty context, and no
// error on malformed input. There is no EIP for it yet; it exists to compare
// a hash-based option with ML-DSA. It is not activated in any fork.
type slhdsaVerify struct{}

const (
	slhdsaSha2128sPkLen  = 32
	slhdsaSha2128sSigLen = 7856
)

// RequiredGas returns BASE + 12 gas per 32-byte word of message, the same
// per-word price as the SHA256 precompile, since the message is hashed once
// with SHA-256.
func (c *slhdsaVerify) RequiredGas(input []byte) uint64 {
	msgLen := len(input) - slhdsaSha2128sPkLen - slhdsaSha2128sSigLen
	if msgLen <= 0 {
		return params.SLHDSASha2128sVerifyBaseGas
	}
	return params.SLHDSASha2128sVerifyBaseGas + toWordSize(uint64(msgLen))*params.SLHDSAVerifyPerWordGas
}

func (c *slhdsaVerify) Run(input []byte) ([]byte, error) {
	if len(input) < slhdsaSha2128sPkLen+slhdsaSha2128sSigLen {
		return common.Hash{}.Bytes(), nil
	}
	var pk slhdsa.PublicKey
	pk.ID = slhdsa.SHA2_128s
	if pk.UnmarshalBinary(input[:slhdsaSha2128sPkLen]) != nil {
		return common.Hash{}.Bytes(), nil
	}
	sig := input[slhdsaSha2128sPkLen : slhdsaSha2128sPkLen+slhdsaSha2128sSigLen]
	msg := slhdsa.NewMessage(input[slhdsaSha2128sPkLen+slhdsaSha2128sSigLen:])
	if slhdsa.Verify(&pk, msg, sig, nil) {
		return true32Byte, nil
	}
	return common.Hash{}.Bytes(), nil
}

func (c *slhdsaVerify) Name() string {
	return "VERIFY_SLHDSA_SHA2_128S"
}
