package stateless

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// MockHeaderReader is a mock implementation of HeaderReader for testing.
type MockHeaderReader struct {
	headers map[common.Hash]*types.Header
}

func (m *MockHeaderReader) GetHeader(hash common.Hash, number uint64) *types.Header {
	return m.headers[hash]
}

func NewMockHeaderReader() *MockHeaderReader {
	return &MockHeaderReader{
		headers: make(map[common.Hash]*types.Header),
	}
}

func (m *MockHeaderReader) AddHeader(header *types.Header) {
	m.headers[header.Hash()] = header
}
