package blsu

import (
	"crypto/rand"
	"fmt"
	kbls "github.com/kilic/bls12-381"
)

type rhsWork struct {
	pub *kbls.PointG1
	msg *kbls.PointG2
}

type lhsWork struct {
	sig *kbls.PointG2
}

// SignatureSetVerify verifies (pubkey,message,signature) tuples as a single batch,
// and is faster than len(inputs) times Verify if the batch is not too small.
//
// The caller should aggregate pubkeys and signatures if they signed the same message,
// this verification is safe to run on duplicate inputs, but not optimized for it.
//
// A completely empty input is also considered to be VALID.
//
// This function parallelizes the scalar-multiplication and signature-aggregation work.
//
// An error is returned if the verification failed due to an operational error,
// e.g. input length mismatch or failing to read entropy bytes with the crypto/rand package.
//
// Original: https://ethresear.ch/t/fast-verification-of-multiple-bls-signatures/5407
func SignatureSetVerify(pubkeys []*Pubkey, messages [][]byte, signatures []*Signature) (bool, error) {
	n := uint(len(pubkeys))
	if uint(len(messages)) != n || uint(len(signatures)) != n {
		return false, fmt.Errorf("input length mismatch: pubs: %d, msgs: %d, sigs: %d", n, len(messages), len(signatures))
	}
	if n == 0 {
		return true, nil
	}
	// Random 64 bits scalars are considered safe, as there is no repeated verification with the same randomness.
	// Fetch all randomness at once, to not cause blocking problems, and not deal with concurrent error handling.
	rngBuf := make([]byte, n*64, n*64)
	// the first entry does not need randomness
	if _, err := rand.Read(rngBuf[64:]); err != nil {
		return false, err
	}
	// return aggregated pubkey, rand-agg. signature, rand-agg. message, error.
	worker := func(offset uint, pubkeys []*Pubkey, messages [][]byte, signatures []*Signature, lhsCh chan<- lhsWork, rhsCh chan<- rhsWork) {
		// scratchpad
		g2 := kbls.NewG2()
		sigCopy := *(*kbls.PointG2)(signatures[0])
		aggSig := &sigCopy
		// error only occurs on invalid domain length
		msg, _ := g2.HashToCurve(messages[0], domain)
		// Optimization: We do not multiply the first signature and message entry with a random scalar,
		// the security depends on not being able to manipulate the delta between the inputs.
		// This only applies to the first worker
		if offset != 0 {
			var randScalar kbls.Fr
			randScalar.FromBytes(rngBuf[offset : offset+64]) // TODO: 64 bytes avoids modulo bias, but maybe we should just use the random Fr func (more calls to RNG, but less data)
			offset += 64
			g2.MulScalar(aggSig, aggSig, &randScalar)
			g2.MulScalar(msg, msg, &randScalar)
		}
		rhsCh <- rhsWork{(*kbls.PointG1)(pubkeys[0]), msg}

		var tmpSig kbls.PointG2
		var randScalar kbls.Fr
		for i := 1; i < len(pubkeys); i++ {
			randScalar.FromBytes(rngBuf[offset : offset+64])
			offset += 64

			tmpSig = *(*kbls.PointG2)(signatures[i])
			g2.MulScalar(&tmpSig, &tmpSig, &randScalar)
			g2.Add(aggSig, aggSig, &tmpSig)

			// error only occurs on invalid domain length
			msg, _ := g2.HashToCurve(messages[i], domain)
			g2.MulScalar(msg, msg, &randScalar)
			pub := (*kbls.PointG1)(pubkeys[i])
			rhsCh <- rhsWork{pub, msg}
		}
		lhsCh <- lhsWork{&sigCopy}
	}

	// TODO: adjust worker count dynamically based on environment
	workerCount := uint(4)
	if n < workerCount {
		workerCount = n
	}

	// Note: the channel buffers are big enough to collect all work,
	// and thus empty one channel before the other, avoiding deadlocks
	lhsCh := make(chan lhsWork, workerCount)
	rhsCh := make(chan rhsWork, n)

	for i := uint(0); i < workerCount; i++ {
		start := n * i / workerCount
		end := (n * (i + 1)) / workerCount
		go worker(start*64, pubkeys[start:end], messages[start:end], signatures[start:end], lhsCh, rhsCh)
	}

	// scratchpad
	g2 := kbls.NewG2()
	eng := kbls.NewEngine()

	aggSig := (<-lhsCh).sig
	for i := uint(1); i < workerCount; i++ {
		g2.Add(aggSig, aggSig, (<-lhsCh).sig)
	}
	for i := uint(0); i < n; i++ {
		pair := <-rhsCh
		eng.AddPair(pair.pub, pair.msg)
	}
	eng.AddPairInv(&kbls.G1One, aggSig)
	return eng.Check(), nil
}

type SignatureSet struct {
	pubkeys    []*Pubkey
	messages   [][]byte
	signatures []*Signature
}

func (s *SignatureSet) Add(pubkey *Pubkey, message []byte, signature *Signature) {
	s.pubkeys = append(s.pubkeys, pubkey)
	s.messages = append(s.messages, message)
	s.signatures = append(s.signatures, signature)
}

func (s *SignatureSet) Verify() bool {
	valid, _ := SignatureSetVerify(s.pubkeys, s.messages, s.signatures)
	return valid
}
