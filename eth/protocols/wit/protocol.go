package wit

import (
	"errors"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/stateless"
)

// Constants to match up protocol versions and messages
const (
	WIT0 = 1
	WIT1 = 2
)

// ProtocolName is the official short name of the `wit` protocol used during
// devp2p capability negotiation.
const ProtocolName = "wit"

// ProtocolVersions are the supported versions of the `wit` protocol (first
// is primary).
var ProtocolVersions = []uint{WIT1, WIT0}

// protocolLengths are the number of implemented message corresponding to
// different protocol versions.
var protocolLengths = map[uint]uint64{WIT1: 6, WIT0: 4}

// maxMessageSize is the maximum cap on the size of a protocol message.
const maxMessageSize = 16 * 1024 * 1024

const (
	NewWitnessMsg         = 0x00
	NewWitnessHashesMsg   = 0x01
	GetMsgWitness         = 0x02
	MsgWitness            = 0x03
	GetWitnessMetadataMsg = 0x04
	WitnessMetadataMsg    = 0x05
)

var (
	errMsgTooLarge    = errors.New("message too long")
	errDecode         = errors.New("invalid message")
	errInvalidMsgCode = errors.New("invalid message code")
)

// Packet represents a p2p message in the `wit` protocol.
type Packet interface {
	Name() string // Name returns a string corresponding to the message type.
	Kind() byte   // Kind returns the message type.
}

// GetWitnessRequest represents a list of witnesses query by witness pages.
type GetWitnessRequest struct {
	WitnessPages []WitnessPageRequest // Request by list of witness pages
}

type WitnessPageRequest struct {
	Hash common.Hash // BlockHash
	Page uint64      // Starts on 0
}

// GetWitnessPacket represents a witness query with request ID wrapping.
type GetWitnessPacket struct {
	RequestId uint64
	*GetWitnessRequest
}

// WitnessPacketRLPPacket represents a witness response with request ID wrapping.
type WitnessPacketRLPPacket struct {
	RequestId uint64
	WitnessPacketResponse
}

// WitnessPacketResponse represents a witness response, to use when we already
// have the witness rlp encoded.
type WitnessPacketResponse []WitnessPageResponse

type WitnessPageResponse struct {
	Data       []byte
	Hash       common.Hash
	Page       uint64 // Starts on 0; If Page >= TotalPages means the request was invalid and the response is an empty data array
	TotalPages uint64 // Length of pages
}

type NewWitnessPacket struct {
	Witness *stateless.Witness
}

type NewWitnessHashesPacket struct {
	Hashes  []common.Hash
	Numbers []uint64
}

// GetWitnessMetadataRequest represents a request for witness metadata (just page count, no data)
type GetWitnessMetadataRequest struct {
	Hashes []common.Hash // Block hashes to get metadata for
}

// GetWitnessMetadataPacket represents a witness metadata query with request ID wrapping
type GetWitnessMetadataPacket struct {
	RequestId uint64
	*GetWitnessMetadataRequest
}

// WitnessMetadataResponse represents a single witness metadata response
type WitnessMetadataResponse struct {
	Hash        common.Hash
	TotalPages  uint64 // Total number of pages for this witness
	WitnessSize uint64 // Total witness size in bytes
	BlockNumber uint64 // Block number this witness belongs to
	Available   bool   // Whether witness exists in database
}

// WitnessMetadataPacket represents a witness metadata response with request ID wrapping
type WitnessMetadataPacket struct {
	RequestId uint64
	Metadata  []WitnessMetadataResponse
}

func (w *GetWitnessRequest) Name() string { return "GetWitness" }
func (w *GetWitnessRequest) Kind() byte   { return GetMsgWitness }

func (*WitnessPacketRLPPacket) Name() string { return "Witness" }
func (*WitnessPacketRLPPacket) Kind() byte   { return MsgWitness }

func (w *NewWitnessPacket) Name() string { return "NewWitness" }
func (w *NewWitnessPacket) Kind() byte   { return NewWitnessMsg }

func (w *NewWitnessHashesPacket) Name() string { return "NewWitnessHashes" }
func (w *NewWitnessHashesPacket) Kind() byte   { return NewWitnessHashesMsg }

func (w *GetWitnessMetadataRequest) Name() string { return "GetWitnessMetadata" }
func (w *GetWitnessMetadataRequest) Kind() byte   { return GetWitnessMetadataMsg }

func (w *WitnessMetadataPacket) Name() string { return "WitnessMetadata" }
func (w *WitnessMetadataPacket) Kind() byte   { return WitnessMetadataMsg }
