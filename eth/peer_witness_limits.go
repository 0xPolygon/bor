package eth

import (
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/eth/protocols/wit"
)

// checkReceivedWitnessPage reports a violation when page does not answer one
// of reqs, is out of its own page range, or is beyond what an honest server
// sends. handleGetWitness echoes each requested (hash, page), so any other
// entry must not be reassembled or handed to the caller as a witness it never
// asked for.
func (p *ethPeer) checkReceivedWitnessPage(reqs []wit.WitnessPageRequest, page wit.WitnessPageResponse, downloadPaused map[common.Hash]bool, mapsMu *sync.RWMutex, jailPeer func(string)) error {
	if !witnessPageRequested(reqs, page) {
		return p.jailPeerForViolation(jailPeer, "unrequested witness page", map[string]interface{}{
			"hash": page.Hash,
			"page": page.Page,
		})
	}
	if page.Page >= page.TotalPages {
		return p.jailPeerForViolation(jailPeer, "invalid page number", map[string]interface{}{
			"hash":       page.Hash,
			"page":       page.Page,
			"totalPages": page.TotalPages,
		})
	}
	return p.checkWitnessPageServeLimits(page, downloadPaused, mapsMu, jailPeer)
}

// witnessPageRequested reports whether page answers one of reqs.
func witnessPageRequested(reqs []wit.WitnessPageRequest, page wit.WitnessPageResponse) bool {
	for _, r := range reqs {
		if r.Hash == page.Hash && r.Page == page.Page {
			return true
		}
	}
	return false
}

// witnessPageWithinServeLimits reports whether a page fits what an honest
// server sends: handleGetWitness cuts pages at PageSize and refuses stored
// witnesses of MaximumCachedWitnessOnARequest or more, and in-flight bodies are
// bounded by MaxWitnessSize, so it never reports more than maxRelayFetchPages
// pages. This bounds the bytes held for reassembly.
func witnessPageWithinServeLimits(page wit.WitnessPageResponse) bool {
	return len(page.Data) <= PageSize && page.TotalPages <= maxRelayFetchPages
}

// checkWitnessPageServeLimits pauses the download of a page's witness and
// reports a violation when the page is beyond what an honest server sends.
func (p *ethPeer) checkWitnessPageServeLimits(page wit.WitnessPageResponse, downloadPaused map[common.Hash]bool, mapsMu *sync.RWMutex, jailPeer func(string)) error {
	if witnessPageWithinServeLimits(page) {
		return nil
	}
	mapsMu.Lock()
	downloadPaused[page.Hash] = true
	mapsMu.Unlock()
	return p.jailPeerForViolation(jailPeer, "witness page beyond serve limits", map[string]interface{}{
		"hash":       page.Hash,
		"totalPages": page.TotalPages,
		"size":       len(page.Data),
	})
}
