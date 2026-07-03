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
