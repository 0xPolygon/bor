//go:build lls_shadow

package types

import "github.com/ethereum/go-ethereum/common"

// ShadowSetSender pre-fills tx's sender cache so every types.Sender(signer,
// tx) call returns from without recovering the (fake) signature. Only
// exists in -tags lls_shadow binaries (LLS shadow-producer experiment).
func ShadowSetSender(tx *Transaction, signer Signer, from common.Address) {
	tx.from.Store(&sigCache{signer: signer, from: from})
}
