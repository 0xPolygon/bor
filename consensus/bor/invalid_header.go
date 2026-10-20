// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
// The go-ethereum library is distributed under the GNU Lesser General Public License v3.0.

package bor

import (
	"errors"

	"github.com/ethereum/go-ethereum/consensus"
)

// IsObjectivelyInvalidHeader classifies intrinsic verification failures.
// The parent must already be locally verified: seal errors can originate there.
// Clock, Heimdall, local-state and unrecognised failures remain neutral.
func IsObjectivelyInvalidHeader(err error) bool {
	if err == nil {
		return false
	}
	for _, sentinel := range []error{
		errMissingVanity, errMissingSignature, errNonCanonicalSeal,
		errInvalidMixDigest, errInvalidUncleHash, errInvalidDifficulty,
		consensus.ErrInvalidNumber,
	} {
		if errors.Is(err, sentinel) {
			return true
		}
	}
	return false
}
