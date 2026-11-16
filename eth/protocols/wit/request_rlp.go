package wit

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rlp"
)

// DecodeRLP decodes a GetWitness packet without relying on promoted methods
// from the embedded request pointer.
func (p *GetWitnessPacket) DecodeRLP(s *rlp.Stream) error {
	if _, err := s.List(); err != nil {
		return err
	}
	if err := s.Decode(&p.RequestId); err != nil {
		return err
	}
	req := new(GetWitnessRequest)
	if err := s.Decode(req); err != nil {
		return err
	}
	p.GetWitnessRequest = req
	return s.ListEnd()
}

// DecodeRLP decodes a witness page request with a streaming element-count cap.
func (r *GetWitnessRequest) DecodeRLP(s *rlp.Stream) error {
	r.WitnessPages = nil
	if _, err := s.List(); err != nil {
		return err
	}
	if _, err := s.List(); err != nil {
		return err
	}
	for len(r.WitnessPages) < MaxWitnessServe && s.MoreDataInList() {
		var page WitnessPageRequest
		if err := s.Decode(&page); err != nil {
			return fmt.Errorf("witness page %d: %w", len(r.WitnessPages), err)
		}
		r.WitnessPages = append(r.WitnessPages, page)
	}
	if s.MoreDataInList() {
		return fmt.Errorf("witness request exceeds %d page limit", MaxWitnessServe)
	}
	if err := s.ListEnd(); err != nil {
		return err
	}
	return s.ListEnd()
}

// DecodeRLP decodes a GetWitnessMetadata packet without relying on promoted
// methods from the embedded request pointer.
func (p *GetWitnessMetadataPacket) DecodeRLP(s *rlp.Stream) error {
	if _, err := s.List(); err != nil {
		return err
	}
	if err := s.Decode(&p.RequestId); err != nil {
		return err
	}
	req := new(GetWitnessMetadataRequest)
	if err := s.Decode(req); err != nil {
		return err
	}
	p.GetWitnessMetadataRequest = req
	return s.ListEnd()
}

// DecodeRLP decodes a witness metadata request with a streaming element-count cap.
func (r *GetWitnessMetadataRequest) DecodeRLP(s *rlp.Stream) error {
	r.Hashes = nil
	if _, err := s.List(); err != nil {
		return err
	}
	if _, err := s.List(); err != nil {
		return err
	}
	for len(r.Hashes) < MaxWitnessMetadataServe && s.MoreDataInList() {
		var hash common.Hash
		if err := s.Decode(&hash); err != nil {
			return fmt.Errorf("witness metadata hash %d: %w", len(r.Hashes), err)
		}
		r.Hashes = append(r.Hashes, hash)
	}
	if s.MoreDataInList() {
		return fmt.Errorf("witness metadata request exceeds %d hash limit", MaxWitnessMetadataServe)
	}
	if err := s.ListEnd(); err != nil {
		return err
	}
	return s.ListEnd()
}

// DecodeRLP decodes a witness response, capping the page count at what a
// request may ask for: an honest server answers one entry per requested page.
func (p *WitnessPacketRLPPacket) DecodeRLP(s *rlp.Stream) error {
	if _, err := s.List(); err != nil {
		return err
	}
	if err := s.Decode(&p.RequestId); err != nil {
		return err
	}
	pages, err := decodeCappedList[WitnessPageResponse](s, MaxWitnessServe, "witness response pages")
	if err != nil {
		return err
	}
	p.WitnessPacketResponse = pages
	return s.ListEnd()
}

// DecodeRLP decodes a witness metadata response, capping the entry count at
// what a request may ask for: an honest server answers one entry per hash.
func (p *WitnessMetadataPacket) DecodeRLP(s *rlp.Stream) error {
	if _, err := s.List(); err != nil {
		return err
	}
	if err := s.Decode(&p.RequestId); err != nil {
		return err
	}
	metadata, err := decodeCappedList[WitnessMetadataResponse](s, MaxWitnessMetadataServe, "witness metadata response")
	if err != nil {
		return err
	}
	p.Metadata = metadata
	return s.ListEnd()
}

// DecodeRLP decodes an unsigned witness announcement with capped list lengths.
func (p *NewWitnessHashesPacket) DecodeRLP(s *rlp.Stream) error {
	if _, err := s.List(); err != nil {
		return err
	}
	hashes, err := decodeCappedList[common.Hash](s, MaxWitnessHashesPerPacket, "witness announcement hashes")
	if err != nil {
		return err
	}
	numbers, err := decodeCappedList[uint64](s, MaxWitnessHashesPerPacket, "witness announcement numbers")
	if err != nil {
		return err
	}
	p.Hashes, p.Numbers = hashes, numbers
	return s.ListEnd()
}

// decodeCappedList decodes an RLP list of T, failing before decoding the
// element that would take it past limit.
func decodeCappedList[T any](s *rlp.Stream, limit int, what string) ([]T, error) {
	if _, err := s.List(); err != nil {
		return nil, err
	}
	items := make([]T, 0)
	for s.MoreDataInList() {
		if len(items) == limit {
			return nil, fmt.Errorf("%s exceed %d item limit", what, limit)
		}
		var item T
		if err := s.Decode(&item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, s.ListEnd()
}

// DecodeRLP decodes signed witness announcements, capping their count before
// any of them is decoded or verified.
func (p *SignedNewWitnessHashesPacket) DecodeRLP(s *rlp.Stream) error {
	if _, err := s.List(); err != nil {
		return err
	}
	announcements, err := decodeCappedList[SignedWitnessAnnouncement](s, MaxSignedAnnouncesPerPacket, "signed witness announcements")
	if err != nil {
		return err
	}
	p.Announcements = announcements
	return s.ListEnd()
}

// DecodeRLP decodes a signed witness announcement, refusing a signature longer
// than SignatureLength before reading it. Shorter signatures are left to
// signature verification, which penalizes the sender without disconnecting.
func (a *SignedWitnessAnnouncement) DecodeRLP(s *rlp.Stream) error {
	if _, err := s.List(); err != nil {
		return err
	}
	if err := s.Decode(&a.BlockHash); err != nil {
		return err
	}
	if err := s.Decode(&a.BlockNumber); err != nil {
		return err
	}
	if err := s.Decode(&a.WitnessHash); err != nil {
		return err
	}
	if err := s.Decode(&a.WitnessSize); err != nil {
		return err
	}
	if _, size, err := s.Kind(); err != nil {
		return err
	} else if size > SignatureLength {
		return fmt.Errorf("witness announcement signature exceeds %d bytes", SignatureLength)
	}
	signature, err := s.Bytes()
	if err != nil {
		return err
	}
	a.Signature = signature
	return s.ListEnd()
}
