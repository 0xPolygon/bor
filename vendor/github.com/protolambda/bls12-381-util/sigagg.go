package blsu

import (
	"crypto/rand"
	"errors"
	"fmt"
	kbls "github.com/kilic/bls12-381"
	"sync"
)

type DeferBLS interface {
	// AggregateVerify checks an aggregated signature over several (PK, message) pairs,
	// and may be deferred until Check().
	AggregateVerify(pubkeys []*Pubkey, messages [][]byte, signature *Signature) error
	// Verify checks an aggregated signature over several (PK, message) pairs,
	// and may be deferred until Check().
	Verify(pk *Pubkey, message []byte, signature *Signature) error
	// FastAggregateVerify verifies the aggregate of multiple signatures on the same message,
	// and may be deferred until Check().
	FastAggregateVerify(pubkeys []*Pubkey, message []byte, signature *Signature) error
	// Eth2FastAggregateVerify wraps FastAggregateVerify and accepts
	// the G2_POINT_AT_INFINITY signature when pubkeys is empty,
	// and may be deferred until Check().
	Eth2FastAggregateVerify(pubkeys []*Pubkey, message []byte, signature *Signature) error
	// Check checks if the aggregate deferred BLS signatures are valid
	Check() error
}

type aggregateCheck struct {
	sync.Mutex
	eng    *kbls.Engine
	aggSig *kbls.PointG2
	// scratchpads
	g1 *kbls.G1
	g2 *kbls.G2
}

// NewAggregateCheck returns a signature-set that implements DeferBLS
func NewAggregateCheck() DeferBLS {
	var aggSig kbls.PointG2
	aggSig.Zero()
	return &aggregateCheck{
		eng:    kbls.NewEngine(),
		aggSig: &aggSig,
		g1:     kbls.NewG1(),
		g2:     kbls.NewG2(),
	}
}

func (a *aggregateCheck) coreAggregateVerify(pubkeys []*Pubkey, messages [][]byte, signature *Signature) error {
	// Precondition: n >= 1, otherwise return INVALID.
	n := uint64(len(messages))
	if n == 0 {
		return errors.New("coreAggregateVerify: Precondition: n >= 1, otherwise return INVALID")
	}
	// implicit in spec: pubkeys and messages lengths must be equal
	if uint64(len(pubkeys)) != n {
		return errors.New("coreAggregateVerify: pubkeys and messages lengths must be equal")
	}

	// 1.  R = signature_to_point(signature)
	R := (*kbls.PointG2)(signature)
	// 2.  If R is INVALID, return INVALID
	// 3.  If signature_subgroup_check(R) is INVALID, return INVALID
	// 2 and 3 are part of the signature deserialization

	var randScalar kbls.Fr
	_, err := randScalar.Rand(rand.Reader)
	if err != nil {
		return errors.New("failed to get random scalar for aggregateCheck.coreAggregateVerify")
	}

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
			return fmt.Errorf("identity pubkey (%d)", i)
		}
		// 8. Q = hash_to_point(message_i)
		Q, err := a.g2.HashToCurve(messages[i], domain)
		if err != nil {
			// e.g. when the domain is too long. Maybe change to panic if never due to a usage error?
			return fmt.Errorf("fail to hash message to g2: %v", err)
		}

		// 9. C1 = C1 * pairing(Q, xP)
		// aggregateCheck change: mul msg with the rand scalar
		a.g2.MulScalar(Q, Q, &randScalar)
		a.eng.AddPair(xP, Q)
	}
	// 10. C2 = pairing(R, P)
	// aggregateCheck change: mul sig with the rand scalar, and aggregate to defer the pairing till Check()
	var Rcpy kbls.PointG2
	a.g2.MulScalar(&Rcpy, R, &randScalar)
	a.g2.Add(a.aggSig, a.aggSig, &Rcpy)

	// 11. If C1 == C2, return VALID, else return INVALID
	// deferred to a.Check()
	return nil
}

func (a *aggregateCheck) AggregateVerify(pubkeys []*Pubkey, messages [][]byte, signature *Signature) error {
	a.Lock()
	defer a.Unlock()
	return a.coreAggregateVerify(pubkeys, messages, signature)
}

func (a *aggregateCheck) coreVerify(pk *Pubkey, message []byte, signature *Signature) error {
	// 1. R = signature_to_point(signature)
	R := (*kbls.PointG2)(signature)
	// 2. If R is INVALID, return INVALID
	// 3. If signature_subgroup_check(R) is INVALID, return INVALID
	// 4. If KeyValidate(PK) is INVALID, return INVALID
	// steps 2-4 are part of bytes -> *Signature deserialization
	if (*kbls.G2)(nil).IsZero(R) {
		// KeyValidate is assumed through deserialization of Pubkey and Signature,
		// but the identity pubkey/signature case is not part of that, thus verify here.
		return errors.New("coreVerify: pubkey cannot be identity pubkey")
	}

	// 5. xP = pubkey_to_point(PK)
	xP := (*kbls.PointG1)(pk)
	// 6. Q = hash_to_point(message)
	Q, err := a.g2.HashToCurve(message, domain)
	if err != nil {
		// e.g. when the domain is too long. Maybe change to panic if never due to a usage error?
		return fmt.Errorf("coreVerify: failed to hash message to g2: %v", err)
	}
	var randScalar kbls.Fr
	_, err = randScalar.Rand(rand.Reader)
	if err != nil {
		return errors.New("failed to get random scalar for aggregateCheck.coreVerify")
	}

	// 7. C1 = pairing(Q, xP)
	// aggregateCheck change: mul msg with the rand scalar
	a.g2.MulScalar(Q, Q, &randScalar)
	a.eng.AddPair(xP, Q)
	// 8. C2 = pairing(R, P)
	// aggregateCheck change: mul sig with the rand scalar, and aggregate to defer the pairing till Check()
	var Rcpy kbls.PointG2
	a.g2.MulScalar(&Rcpy, R, &randScalar)
	a.g2.Add(a.aggSig, a.aggSig, &Rcpy)

	// 9. If C1 == C2, return VALID, else return INVALID
	// deferred to a.Check()
	return nil
}

func (a *aggregateCheck) Verify(pk *Pubkey, message []byte, signature *Signature) error {
	a.Lock()
	defer a.Unlock()
	return a.coreVerify(pk, message, signature)
}

func (a *aggregateCheck) FastAggregateVerify(pubkeys []*Pubkey, message []byte, signature *Signature) error {
	a.Lock()
	defer a.Unlock()
	// Precondition: n >= 1, otherwise return INVALID.
	n := uint64(len(pubkeys))
	if n == 0 {
		return errors.New("pubkeys list cannot be empty")
	}
	// Procedure:
	// 1. aggregate = pubkey_to_point(PK_1)
	// copy the first pubkey
	aggregate := *(*kbls.PointG1)(pubkeys[0])
	// check identity pubkey
	if (*kbls.G1)(nil).IsZero(&aggregate) {
		return fmt.Errorf("pubkey 0 cannot be zero")
	}
	// 2. for i in 2, ..., n:
	for i := uint64(1); i < n; i++ {
		// 3. next = pubkey_to_point(PK_i)
		next := (*kbls.PointG1)(pubkeys[i])
		// check identity pubkey
		if (*kbls.G1)(nil).IsZero(next) {
			return fmt.Errorf("pubkey %d cannot be zero", i)
		}
		// 4. aggregate = aggregate + next
		a.g1.Add(&aggregate, &aggregate, next)
	}
	// 5. PK = point_to_pubkey(aggregate)
	PK := (*Pubkey)(&aggregate)
	// 6. return coreVerify(PK, message, signature)
	return a.coreVerify(PK, message, signature)
}

func (a *aggregateCheck) Eth2FastAggregateVerify(pubkeys []*Pubkey, message []byte, signature *Signature) error {
	// no lock, state is not accessed and inner FastAggregateVerify locks
	if len(pubkeys) == 0 && (*kbls.G2)(nil).IsZero((*kbls.PointG2)(signature)) {
		return nil
	}
	return a.FastAggregateVerify(pubkeys, message, signature)
}

// Check will reset the AggregateCheck after determining the result
func (a *aggregateCheck) Check() error {
	a.Lock()
	defer a.Unlock()
	a.eng.AddPairInv(&kbls.G1One, a.aggSig)
	res := a.eng.Check()
	a.eng.Reset()
	a.aggSig.Zero()
	if res {
		return nil
	} else {
		return errors.New("invalid aggregate signature")
	}
}

var _ DeferBLS = (*aggregateCheck)(nil)

// ImmediateCheck implements DeferBLS without deferring anything, i.e. signature checks will be performed immediately.
type ImmediateCheck struct{}

func (i ImmediateCheck) AggregateVerify(pubkeys []*Pubkey, messages [][]byte, signature *Signature) error {
	if AggregateVerify(pubkeys, messages, signature) {
		return nil
	} else {
		return errors.New("invalid signature")
	}
}

func (i ImmediateCheck) Verify(pk *Pubkey, message []byte, signature *Signature) error {
	if Verify(pk, message, signature) {
		return nil
	} else {
		return errors.New("invalid signature")
	}
}

func (i ImmediateCheck) FastAggregateVerify(pubkeys []*Pubkey, message []byte, signature *Signature) error {
	if FastAggregateVerify(pubkeys, message, signature) {
		return nil
	} else {
		return errors.New("invalid signature")
	}
}

func (i ImmediateCheck) Eth2FastAggregateVerify(pubkeys []*Pubkey, message []byte, signature *Signature) error {
	if Eth2FastAggregateVerify(pubkeys, message, signature) {
		return nil
	} else {
		return errors.New("invalid signature")
	}
}

func (i ImmediateCheck) Check() error {
	return nil
}

var _ DeferBLS = (*ImmediateCheck)(nil)
