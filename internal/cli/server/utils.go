package server

import (
	"net/http"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/internal/cli/server/proto"
	"github.com/ethereum/go-ethereum/p2p"

	protobor "github.com/0xPolygon/polyproto/bor"
	protocommon "github.com/0xPolygon/polyproto/common"
	protoutil "github.com/0xPolygon/polyproto/utils"
)

func PeerInfoToPeer(info *p2p.PeerInfo) *proto.Peer {
	return &proto.Peer{
		Id:      info.ID,
		Enode:   info.Enode,
		Enr:     info.ENR,
		Caps:    info.Caps,
		Name:    info.Name,
		Trusted: info.Network.Trusted,
		Static:  info.Network.Static,
	}
}

func ConvertBloomToProtoBloom(bloom types.Bloom) *protobor.Bloom {
	return &protobor.Bloom{
		Bloom: bloom.Bytes(),
	}
}

func ConvertLogsToProtoLogs(logs []*types.Log) []*protobor.Log {
	var protoLogs []*protobor.Log
	for _, log := range logs {
		protoLog := &protobor.Log{
			Address:     protoutil.ConvertAddressToH160(log.Address),
			Topics:      ConvertTopicsToProtoTopics(log.Topics),
			Data:        log.Data,
			BlockNumber: log.BlockNumber,
			TxHash:      protoutil.ConvertHashToH256(log.TxHash),
			TxIndex:     uint64(log.TxIndex),
			BlockHash:   protoutil.ConvertHashToH256(log.BlockHash),
			Index:       uint64(log.Index),
			Removed:     log.Removed,
		}
		protoLogs = append(protoLogs, protoLog)
	}

	return protoLogs
}

func ConvertTopicsToProtoTopics(topics []common.Hash) []*protocommon.H256 {
	var protoTopics []*protocommon.H256
	for _, topic := range topics {
		protoTopics = append(protoTopics, protoutil.ConvertHashToH256(topic))
	}

	return protoTopics
}

func ConvertReceiptToProtoReceipt(receipt *types.Receipt) *protobor.Receipt {
	return &protobor.Receipt{
		Type:              uint64(receipt.Type),
		PostState:         receipt.PostState,
		Status:            receipt.Status,
		CumulativeGasUsed: receipt.CumulativeGasUsed,
		Bloom:             ConvertBloomToProtoBloom(receipt.Bloom),
		Logs:              ConvertLogsToProtoLogs(receipt.Logs),
		TxHash:            protoutil.ConvertHashToH256(receipt.TxHash),
		ContractAddress:   protoutil.ConvertAddressToH160(receipt.ContractAddress),
		GasUsed:           receipt.GasUsed,
		EffectiveGasPrice: receipt.EffectiveGasPrice.Int64(),
		BlobGasUsed:       receipt.BlobGasUsed,
		BlockHash:         protoutil.ConvertHashToH256(receipt.BlockHash),
		BlockNumber:       receipt.BlockNumber.Int64(),
		TransactionIndex:  uint64(receipt.TransactionIndex),
	}
}

// HealthStatus represents the health status with level, code, and message
type HealthStatus struct {
	Level   HealthStatusLevel `json:"level"`
	Code    int               `json:"code"`
	Message string            `json:"message"`
}

// HealthStatusLevel represents the health status level.
type HealthStatusLevel int

const (
	StatusOK HealthStatusLevel = iota
	StatusWarn
	StatusCritical
)

// String returns the string representation of the health status level.
func (h HealthStatusLevel) String() string {
	switch h {
	case StatusOK:
		return "OK"
	case StatusWarn:
		return "WARN"
	case StatusCritical:
		return "CRITICAL"
	default:
		return "UNKNOWN"
	}
}

// Code returns the numeric code for the health status level.
func (h HealthStatusLevel) Code() int {
	switch h {
	case StatusOK:
		return 0
	case StatusWarn:
		return 1
	case StatusCritical:
		return 2
	default:
		return -1
	}
}

// MarshalJSON implements json.Marshaler interface to return the string representation of the health status level.
func (h HealthStatusLevel) MarshalJSON() ([]byte, error) {
	return []byte(`"` + h.String() + `"`), nil
}

// ResponseRecorder captures the response from health-go handler.
type ResponseRecorder struct {
	http.ResponseWriter
	statusCode int
	body       []byte
}

func (r *ResponseRecorder) WriteHeader(statusCode int) {
	r.statusCode = statusCode
}

func (r *ResponseRecorder) Write(data []byte) (int, error) {
	r.body = append(r.body, data...)
	return len(data), nil
}
