package blsu

import (
	"errors"
	kbls "github.com/kilic/bls12-381"
)

// IETF signature draft v4:
// https://datatracker.ietf.org/doc/html/draft-irtf-cfrg-bls-signature-04
//

var domain = []byte("BLS_SIG_BLS12381G2_XMD:SHA-256_SSWU_RO_POP_")

// cipher-suite: BLS_SIG_BLS12381G2_XMD:SHA-256_SSWU_RO_POP_
// BLS_SIG_
// BLS12381G2_XMD:SHA-256_SSWU_RO  # hash to curve suite, G2, SHA-256
// _
// POP  # proof-of-possession scheme
// _

// *  hash_to_point: a hash from arbitrary strings to elliptic curve
// points. hash_to_point MUST be defined in terms of a hash-to-curve
// suite [I-D.irtf-cfrg-hash-to-curve].
//
// The RECOMMENDED hash-to-curve domain separation tag is the
// ciphersuite ID string defined above.
//
// *  hash_pubkey_to_point (only specified when SC is proof-of-
// possession): a hash from serialized public keys to elliptic curve
// points. hash_pubkey_to_point MUST be defined in terms of a hash-
// to-curve suite [I-D.irtf-cfrg-hash-to-curve].
//
// The hash-to-curve domain separation tag MUST be distinct from the
// domain separation tag used for hash_to_point.  It is RECOMMENDED
// that the domain separation tag be constructed similarly to the
// ciphersuite ID string, namely:
// "BLS_POP_" || H2C_SUITE_ID || SC_TAG || "_"

type Pubkey kbls.PointG1

// Serialize to compressed point
func (pub *Pubkey) Serialize() (out [48]byte) {
	// TODO: in Go 1.17 this copy can be avoided with a slice->array cast
	copy(out[:], kbls.NewG1().ToCompressed((*kbls.PointG1)(pub)))
	return
}

// Deserialize compressed point.
// Performs deserialization, a subgroup check, but not a full KeyValidate: the identity pubkey is allowed.
// Functions that are specified to perform a KeyValidate on a Pubkey can ignore it, after deserializing a valid *Pubkey,
// EXCEPT the identity pubkey check.
func (pub *Pubkey) Deserialize(in *[48]byte) error {
	// includes sub-group check (TODO: is this check correct and complete though?)
	p, err := kbls.NewG1().FromCompressed(in[:])
	if err != nil {
		return err
	}
	*pub = (Pubkey)(*p)
	return nil
}

type Signature kbls.PointG2

// Serialize to compressed point
func (sig *Signature) Serialize() (out [96]byte) {
	// TODO: in Go 1.17 this copy can be avoided with a slice->array cast
	copy(out[:], kbls.NewG2().ToCompressed((*kbls.PointG2)(sig)))
	return
}

// Deserialize compressed point
func (sig *Signature) Deserialize(in *[96]byte) error {
	// includes sub-group check
	p, err := kbls.NewG2().FromCompressed(in[:])
	if err != nil {
		return err
	}
	*sig = (Signature)(*p)
	return nil
}

type SecretKey kbls.Fr

// Serialize to big-endian serialized integer
func (sk *SecretKey) Serialize() (out [32]byte) {
	// ToBytes output is always 32 bytes, no need for extra padding or alignment work
	copy(out[:], ((*kbls.Fr)(sk)).ToBytes())
	return
}

// Deserialize big-endian serialized integer. A modulo r is applied to out-of-range keys.
func (sk *SecretKey) Deserialize(in *[32]byte) error {
	(*kbls.Fr)(sk).FromBytes(in[:])
	// KeyGen states: a uniformly random integer such that 1 <= SK < r.
	if ((*kbls.Fr)(sk)).IsZero() {
		return errors.New("secret key may not be zero")
	}
	return nil
}

// The SkToPk algorithm takes a secret key SK and outputs the corresponding public key PK.
func SkToPk(sk *SecretKey) (*Pubkey, error) {
	// a secret integer such that 1 <= SK < r.
	if ((*kbls.Fr)(sk)).IsZero() {
		return nil, errors.New("secret key may not be zero")
	}

	// 1. xP = SK * P
	var xP kbls.PointG1
	g1 := kbls.NewG1()
	g1.MulScalar(&xP, &kbls.G1One, (*kbls.Fr)(sk))
	// 2. PK = point_to_pubkey(xP)
	PK := (*Pubkey)(&xP)
	// 3. return PK
	return PK, nil
}

// TODO: unsupported, should be part of bytes->Pubkey deserialization
//
//// The KeyValidate algorithm ensures that a public key is valid.  In
//// particular, it ensures that a public key represents a valid, non-
//// identity point that is in the correct subgroup.
//func KeyValidate(pub []byte) bool {
//	// 1. xP = pubkey_to_point(PK)
//	// 2. If xP is INVALID, return INVALID
//	// 3. If xP is the identity element, return INVALID
//	// 4. If pubkey_subgroup_check(xP) is INVALID, return INVALID
//	// 5. return VALID
//	return false
//}

// The coreSign algorithm computes a signature from SK, a secret key, and message, an octet string.
func coreSign(sk *SecretKey, message []byte) *Signature {
	g2 := kbls.NewG2()
	// 1. Q = hash_to_point(message)
	Q, err := g2.HashToCurve(message, domain)
	if err != nil {
		// only when the domain is too long, which we know it is not
		panic(err)
	}
	// 2. R = SK * Q
	var R kbls.PointG2
	g2.MulScalar(&R, Q, (*kbls.Fr)(sk))
	// 3. signature = point_to_signature(R)
	// serialization is deferred, see Signature.Serialize()
	signature := (*Signature)(&R)
	// 4. return signature
	return signature
}

// The coreVerify algorithm checks that a signature is valid for the octet string message under the public key PK.
func coreVerify(pk *Pubkey, message []byte, signature *Signature) bool {
	// 1. R = signature_to_point(signature)
	R := (*kbls.PointG2)(signature)
	// 2. If R is INVALID, return INVALID
	// 3. If signature_subgroup_check(R) is INVALID, return INVALID
	// 4. If KeyValidate(PK) is INVALID, return INVALID
	// steps 2-4 are part of bytes -> *Signature deserialization
	if (*kbls.G2)(nil).IsZero(R) {
		// KeyValidate is assumed through deserialization of Pubkey and Signature,
		// but the identity pubkey/signature case is not part of that, thus verify here.
		return false
	}

	// 5. xP = pubkey_to_point(PK)
	xP := (*kbls.PointG1)(pk)
	// 6. Q = hash_to_point(message)
	Q, err := kbls.NewG2().HashToCurve(message, domain)
	if err != nil {
		// e.g. when the domain is too long. Maybe change to panic if never due to a usage error?
		return false
	}
	// 7. C1 = pairing(Q, xP)
	eng := kbls.NewEngine()
	eng.AddPair(xP, Q)
	// 8. C2 = pairing(R, P)
	P := &kbls.G1One
	eng.AddPairInv(P, R) // inverse, optimization to mul with inverse and check equality to 1
	// 9. If C1 == C2, return VALID, else return INVALID
	return eng.Check()
}

// The Aggregate algorithm aggregates multiple signatures into one.
func Aggregate(signatures []*Signature) (*Signature, error) {
	// Precondition: n >= 1, otherwise return INVALID.
	if len(signatures) == 0 {
		return nil, errors.New("need at least 1 signature")
	}

	// 1. aggregate = signature_to_point(signature_1)
	// make a copy of the first signature
	aggregate := (kbls.PointG2)(*signatures[0])
	// 2. If aggregate is INVALID, return INVALID
	// part of the Signature deserialization

	g2 := kbls.NewG2()
	// 3. for i in 2, ..., n:
	for i := 1; i < len(signatures); i++ {
		// 4. next = signature_to_point(signature_i)
		next := (*kbls.PointG2)(signatures[i])
		// 5. If next is INVALID, return INVALID
		// part of the Signature deserialization
		// 6. aggregate = aggregate + next
		g2.Add(&aggregate, &aggregate, next)
	}
	// 7. signature = point_to_signature(aggregate)
	signature := (*Signature)(&aggregate)
	// 8. return signature
	return signature, nil
}

// The coreAggregateVerify algorithm checks an aggregated signature over several (PK, message) pairs.
func coreAggregateVerify(pubkeys []*Pubkey, messages [][]byte, signature *Signature) bool {
	// Precondition: n >= 1, otherwise return INVALID.
	n := uint64(len(messages))
	if n == 0 {
		return false
	}
	// implicit in spec: pubkeys and messages lengths must be equal
	if uint64(len(pubkeys)) != n {
		return false
	}

	// 1.  R = signature_to_point(signature)
	R := (*kbls.PointG2)(signature)
	// 2.  If R is INVALID, return INVALID
	// 3.  If signature_subgroup_check(R) is INVALID, return INVALID
	// 2 and 3 are part of the signature deserialization

	g2 := kbls.NewG2()
	engine := kbls.NewEngine()
	// 4.  C1 = 1 (the identity element in GT)
	// 5.  for i in 1, ..., n:
	for i := uint64(0); i < n; i++ {
		// 6. If KeyValidate(PK_i) is INVALID, return INVALID
		// part of the pubkey deserialization, except that the spec considers the identity pubkey to fail the KeyValidate,
		// while valid in *Pubkey deserialization. Identity-pubkey check below.

		// 7. xP = pubkey_to_point(PK_i)
		xP := (*kbls.PointG1)(pubkeys[i])
		// check identity pubkey
		if (*kbls.G1)(nil).IsZero(xP) {
			return false
		}
		// 8. Q = hash_to_point(message_i)
		Q, err := g2.HashToCurve(messages[i], domain)
		if err != nil {
			// e.g. when the domain is too long. Maybe change to panic if never due to a usage error?
			return false
		}

		// 9. C1 = C1 * pairing(Q, xP)
		engine.AddPair(xP, Q)
	}
	// 10. C2 = pairing(R, P)
	P := &kbls.G1One
	engine.AddPairInv(P, R)
	// 11. If C1 == C2, return VALID, else return INVALID
	return engine.Check()
}

// In the Proof Of Possession scheme
// The Sign, Verify, and AggregateVerify functions are identical to coreSign, coreVerify, and coreAggregateVerify (Section 2), respectively.

// The AggregateVerify algorithm checks an aggregated signature over several (PK, message) pairs.
func AggregateVerify(pubkeys []*Pubkey, messages [][]byte, signature *Signature) bool {
	return coreAggregateVerify(pubkeys, messages, signature)
}

// The Verify algorithm checks an aggregated signature over several (PK, message) pairs.
func Verify(pk *Pubkey, message []byte, signature *Signature) bool {
	return coreVerify(pk, message, signature)
}

// The Sign algorithm computes a signature from SK, a secret key, and message, an octet string.
func Sign(sk *SecretKey, message []byte) *Signature {
	return coreSign(sk, message)
}

// TODO: do we need the basic-scheme version of AggregateVerify?
//
//// The AggregateVerify function first ensures that all messages are distinct, and then invokes coreAggregateVerify.
////
//// This function only applies to the Basic signature scheme (Proof Of Possession uses coreAggregateVerify directly)
//func AggregateVerify(pubkeys []*Pubkey, messages [][]byte, signature *Signature) bool {
//	// Precondition: n >= 1, otherwise return INVALID.
//	n := uint64(len(messages))
//	if n == 0 {
//		return false
//	}
//
//	// 1. If any two input messages are equal, return INVALID.
//
//	// Sort first, then check if any adjacent messages are equal to spot duplicates
//	sorted := make([][]byte, len(messages), len(messages))
//	copy(sorted, messages)
//	sort.Slice(sorted, func(i, j int) bool {
//		return bytes.Compare(sorted[i], sorted[j]) < 0
//	})
//	for i, j := uint64(0), uint64(1); j < n; i, j = i+1, j+1 {
//		if bytes.Compare(sorted[i], sorted[j]) == 0 {
//			return false
//		}
//	}
//
//	// 2. return coreAggregateVerify((PK_1, ..., PK_n),
//	//                               (message_1, ..., message_n),
//	//                               signature)
//	return coreAggregateVerify(pubkeys, messages, signature)
//}

// FastAggregateVerify is a verification algorithm for the aggregate of multiple signatures on the same message.
// This function is faster than AggregateVerify.
//
// This function applies only to the Proof Of Possession signature scheme.
func FastAggregateVerify(pubkeys []*Pubkey, message []byte, signature *Signature) bool {
	// Precondition: n >= 1, otherwise return INVALID.
	n := uint64(len(pubkeys))
	if n == 0 {
		return false
	}

	g1 := kbls.NewG1()
	// Procedure:
	// 1. aggregate = pubkey_to_point(PK_1)
	// copy the first pubkey
	aggregate := *(*kbls.PointG1)(pubkeys[0])
	// check identity pubkey
	if (*kbls.G1)(nil).IsZero(&aggregate) {
		return false
	}
	// 2. for i in 2, ..., n:
	for i := uint64(1); i < n; i++ {
		// 3. next = pubkey_to_point(PK_i)
		next := (*kbls.PointG1)(pubkeys[i])
		// check identity pubkey
		if (*kbls.G1)(nil).IsZero(next) {
			return false
		}
		// 4. aggregate = aggregate + next
		g1.Add(&aggregate, &aggregate, next)
	}
	// 5. PK = point_to_pubkey(aggregate)
	PK := (*Pubkey)(&aggregate)
	// 6. return coreVerify(PK, message, signature)
	return coreVerify(PK, message, signature)
}

// AggregatePubkeys is specified as `eth2_aggregate_pubkeys` in Eth2, and is the G1 variant of Aggregate in G2.
func AggregatePubkeys(pubkeys []*Pubkey) (*Pubkey, error) {
	// Precondition: n >= 1, otherwise return INVALID.
	if len(pubkeys) == 0 {
		return nil, errors.New("need at least 1 pubkey")
	}

	// 1. aggregate = pubkey_to_point(pubkey_1)
	// make a copy of the first pubkey
	aggregate := (kbls.PointG1)(*pubkeys[0])
	// 2. If aggregate is INVALID, return INVALID
	// part of the Pubkey deserialization

	// check identity pubkey
	// see https://github.com/ethereum/consensus-specs/issues/2538
	if (*kbls.G1)(nil).IsZero(&aggregate) {
		return nil, errors.New("cannot add zero pubkey to aggregate")
	}

	g1 := kbls.NewG1()
	// 3. for i in 2, ..., n:
	for i := 1; i < len(pubkeys); i++ {
		// 4. next = pubkey_to_point(pubkey_i)
		next := (*kbls.PointG1)(pubkeys[i])
		// check identity pubkey
		if (*kbls.G1)(nil).IsZero(next) {
			return nil, errors.New("cannot add zero pubkey to aggregate")
		}
		// 5. If next is INVALID, return INVALID
		// part of the Pubkey deserialization
		// 6. aggregate = aggregate + next
		g1.Add(&aggregate, &aggregate, next)
	}
	// 7. pubkey = point_to_pubkey(aggregate)
	pubkey := (*Pubkey)(&aggregate)
	// 8. return aggregated pubkey
	return pubkey, nil
}

// Wrapper to FastAggregateVerify accepting the G2_POINT_AT_INFINITY signature when pubkeys is empty.
func Eth2FastAggregateVerify(pubkeys []*Pubkey, message []byte, signature *Signature) bool {
	// if len(pubkeys) == 0 and signature == G2_POINT_AT_INFINITY: return True

	// G2_POINT_AT_INFINITY(serialized form is b'\xc0' + b'\x00' * 95, i.e. top 2 bits are one.
	// most significant bit: to indicate it's compressed (if it were serialized)
	// second most signficant bit: to indicate it is at infinity, the rest of the bits must be zero then.

	// The IsZero method does not actually use the G2 scratchpad,
	// so we call into this method with a nil-receiver to avoid unnecessary work.
	if len(pubkeys) == 0 && (*kbls.G2)(nil).IsZero((*kbls.PointG2)(signature)) {
		return true
	}
	return FastAggregateVerify(pubkeys, message, signature)
}
