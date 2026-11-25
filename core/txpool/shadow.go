package txpool

import "github.com/ethereum/go-ethereum/common"

// ShadowRemove drops one transaction from whichever subpool holds it. Used
// only by the LLS shadow-producer experiment.
func (p *TxPool) ShadowRemove(hash common.Hash) bool {
	for _, sub := range p.subpools {
		if r, ok := sub.(interface{ ShadowRemove(common.Hash) bool }); ok && r.ShadowRemove(hash) {
			return true
		}
	}

	return false
}
