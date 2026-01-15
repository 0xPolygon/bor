package eth // replace with the actual package name

import (
	"bytes"
	"errors"
	"math/big"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/eth/protocols/wit"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert" // import path where ethPeer lives
)

func TestRequestWitnesses_NoWitPeer(t *testing.T) {
	p := &ethPeer{}                  // no witPeer set
	dlCh := make(chan *eth.Response) // downstream result channel

	req, err := p.RequestWitnesses([]common.Hash{{1, 2, 3}}, dlCh)

	assert.Nil(t, req, "expected nil *eth.Request when witPeer is missing")
	assert.EqualError(t, err, "witness peer not found")
}

func TestRequestWitnesses_HasWitPeer_Returns(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	hashToRequest := common.Hash{123}
	witness, _ := stateless.NewWitness(&types.Header{}, nil)
	FillWitnessWithDeterministicRandomState(witness, 10*1024)
	var witBuf bytes.Buffer
	witness.EncodeRLP(&witBuf)

	mockWitPeer := NewMockWitnessPeer(ctrl)
	p := &ethPeer{Peer: eth.NewPeer(1, p2p.NewPeer(enode.ID{0x01, 0x02}, "test-peer", []p2p.Cap{}), nil, nil), witPeer: &witPeer{Peer: mockWitPeer}}
	dlCh := make(chan *eth.Response)

	mockWitPeer.EXPECT().Log().Return(log.New()).AnyTimes()
	mockWitPeer.
		EXPECT().
		RequestWitness(gomock.Eq([]wit.WitnessPageRequest{{Hash: hashToRequest, Page: 0}}), gomock.AssignableToTypeOf((chan *wit.Response)(nil))).
		DoAndReturn(func(wpr []wit.WitnessPageRequest, ch chan *wit.Response) (*wit.Request, error) {
			go func() {
				ch <- &wit.Response{
					Res: &wit.WitnessPacketRLPPacket{
						WitnessPacketResponse: []wit.WitnessPageResponse{{Page: 0, TotalPages: 1, Hash: hashToRequest, Data: witBuf.Bytes()}},
					},
					Done: make(chan error, 10), // buffered so no block
				}
			}()

			return &wit.Request{}, nil
		}).
		Times(1)

	req, err := p.RequestWitnesses([]common.Hash{hashToRequest}, dlCh)

	response := <-dlCh
	assert.NoError(t, err)
	assert.NotNil(t, req, "expected a non-nil *eth.Request shim when witPeer is set")
	assert.NotNil(t, response, "expected a non-nil *eth.Response shim when witPeer is set")
}

// Tests an adversarial scenario where multiples requests could be shot at once
// It'll be done by spllitting the payload in multiple different pages and then controlling how much are calling the RequestWitness
func TestRequestWitnesses_Controlling_Max_Concurrent_Calls(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	hashToRequest := common.Hash{123}
	witness, _ := stateless.NewWitness(&types.Header{}, nil)
	FillWitnessWithDeterministicRandomState(witness, 10*1024)
	var witBuf bytes.Buffer
	witness.EncodeRLP(&witBuf)

	mockWitPeer := NewMockWitnessPeer(ctrl)
	p := &ethPeer{Peer: eth.NewPeer(1, p2p.NewPeer(enode.ID{0x01, 0x02}, "test-peer", []p2p.Cap{}), nil, nil), witPeer: &witPeer{Peer: mockWitPeer}}
	dlCh := make(chan *eth.Response)
	concurrentCount := 0
	maxConcurrentCount := 0
	var muConcurrentCount sync.Mutex
	testPageSize := 200                                                   // 200bytes -> ~ 10*1024/200 ~ 54 pages
	totalPages := (len(witBuf.Bytes()) + testPageSize - 1) / testPageSize // ceil division len()/pageSize

	randomPageToFailTwice := rand.Intn(totalPages-1) + 1
	randomFailCount := 0
	zeroPageFailCount := 0 //page zero is edge case so we will also fail this page in all tests
	calls := 0

	mockWitPeer.EXPECT().Log().Return(log.New()).AnyTimes()
	mockWitPeer.
		EXPECT().
		RequestWitness(gomock.AssignableToTypeOf(([]wit.WitnessPageRequest)(nil)), gomock.AssignableToTypeOf((chan *wit.Response)(nil))).
		DoAndReturn(func(wpr []wit.WitnessPageRequest, ch chan *wit.Response) (*wit.Request, error) {
			go func() {
				muConcurrentCount.Lock()
				concurrentCount++
				calls++
				if concurrentCount > maxConcurrentCount {
					maxConcurrentCount = concurrentCount
				}

				shouldFail := false
				if wpr[0].Page == uint64(randomPageToFailTwice) && randomFailCount < 2 {
					shouldFail = true
					randomFailCount++
				}
				if wpr[0].Page == uint64(0) && zeroPageFailCount < 2 {
					shouldFail = true
					zeroPageFailCount++
				}

				muConcurrentCount.Unlock()
				time.Sleep(50 * time.Millisecond) // force wait to increase concurrency
				start := wpr[0].Page * uint64(testPageSize)
				end := start + uint64(testPageSize)
				if end > uint64(len(witBuf.Bytes())) {
					end = uint64(len(witBuf.Bytes()))
				}

				if !shouldFail {
					ch <- &wit.Response{
						Res: &wit.WitnessPacketRLPPacket{
							WitnessPacketResponse: []wit.WitnessPageResponse{{Page: wpr[0].Page, TotalPages: uint64(totalPages), Hash: hashToRequest, Data: witBuf.Bytes()[start:end]}},
						},
						Done: make(chan error, 10), // buffered so no block
					}
				} else {
					ch <- &wit.Response{
						Res:  0,
						Done: make(chan error, 10), // buffered so no block
					}
				}

				muConcurrentCount.Lock()
				concurrentCount--
				muConcurrentCount.Unlock()
			}()

			return &wit.Request{}, nil
		}).
		Times(totalPages + 4) // because of two fails

	req, err := p.RequestWitnesses([]common.Hash{hashToRequest}, dlCh)

	response := <-dlCh
	assert.NoError(t, err)
	assert.NotNil(t, req, "expected a non-nil *eth.Request shim when witPeer is set")
	assert.NotNil(t, response, "expected a non-nil *eth.Response shim when witPeer is set")
	assert.Equal(t, 5, maxConcurrentCount, "must reach the maximum of the concurrent cound")
}

// FillWitnessWithDeterministicRandomState repeatedly generates and adds random code blocks
// to the witness until the total added code reaches 40MB. The size of each block is up to 24KB.
// Random generation is seeded deterministically on each call, so the sequence is repeatable.
func FillWitnessWithDeterministicRandomState(w *stateless.Witness, targetSize int) {
	const (
		maxChunkSize = 24 * 1024 // 24KB
		seed         = 42        // fixed seed for determinism
	)

	r := rand.New(rand.NewSource(seed))
	total := 0

	for total < targetSize {
		// determine next chunk size (1 to maxChunkSize)
		chunkSize := r.Intn(maxChunkSize) + 1
		if total+chunkSize > targetSize {
			chunkSize = targetSize - total
		}

		// generate random bytes
		buf := make([]byte, chunkSize)
		for i := range buf {
			buf[i] = byte(r.Intn(256))
		}

		// add to witness
		states := make(map[string]struct{})
		states[string(buf)] = struct{}{}
		w.AddState(states)
		total += chunkSize
	}
}

// TestRequestWitnesses_PeerDisconnectionNoPanic tests that when a peer disconnects
// before any responses are received, the function gracefully handles the situation
// without panicking due to nil pointer dereference.
func TestRequestWitnesses_PeerDisconnectionNoPanic(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	hashToRequest := common.Hash{123}
	mockWitPeer := NewMockWitnessPeer(ctrl)
	p := &ethPeer{
		Peer:    eth.NewPeer(1, p2p.NewPeer(enode.ID{0x01, 0x02}, "test-peer", []p2p.Cap{}), nil, nil),
		witPeer: &witPeer{Peer: mockWitPeer},
	}
	dlCh := make(chan *eth.Response, 1)

	mockWitPeer.EXPECT().Log().Return(log.New()).AnyTimes()

	// Mock RequestWitness to simulate immediate failure (peer disconnection).
	mockWitPeer.
		EXPECT().
		RequestWitness(gomock.Any(), gomock.Any()).
		DoAndReturn(func(wpr []wit.WitnessPageRequest, ch chan *wit.Response) (*wit.Request, error) {
			// Close the channel immediately to simulate no responses.
			go func() {
				// Immediately close the channel without sending anything.
				close(ch)
			}()
			return &wit.Request{}, nil
		}).
		AnyTimes()

	// Call RequestWitnesses.
	req, err := p.RequestWitnesses([]common.Hash{hashToRequest}, dlCh)

	// Verify no error during setup.
	assert.NoError(t, err)
	assert.NotNil(t, req, "expected a non-nil *eth.Request")

	// Wait for response - should receive empty response instead of panic.
	select {
	case response := <-dlCh:
		assert.NotNil(t, response, "should receive a response")
		assert.NotNil(t, response.Res, "response should have non-nil Res field")

		// Verify it's an empty witness slice, not nil.
		witnesses, ok := response.Res.([]*stateless.Witness)
		assert.True(t, ok, "response should contain []*stateless.Witness")
		assert.Equal(t, 0, len(witnesses), "should receive empty witness slice")

		// Verify other fields are handled correctly.
		assert.NotNil(t, response.Req, "should have request reference")
		assert.NotNil(t, response.Done, "should have Done channel")
		assert.Nil(t, response.Meta, "should have nil Meta when no responses received")
		assert.Equal(t, time.Duration(0), response.Time, "should have zero time when no responses received")

	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for response")
	}
}

// TestRequestWitnesses_EmptyResponsesNoPanic tests the case where responses are received
// but they contain no usable witness data, ensuring no panic occurs.
func TestRequestWitnesses_EmptyResponsesNoPanic(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	hashToRequest := common.Hash{123}
	mockWitPeer := NewMockWitnessPeer(ctrl)
	p := &ethPeer{
		Peer:    eth.NewPeer(1, p2p.NewPeer(enode.ID{0x01, 0x02}, "test-peer", []p2p.Cap{}), nil, nil),
		witPeer: &witPeer{Peer: mockWitPeer},
	}
	dlCh := make(chan *eth.Response, 1)

	mockWitPeer.EXPECT().Log().Return(log.New()).AnyTimes()

	// Mock RequestWitness to send empty/invalid responses.
	mockWitPeer.
		EXPECT().
		RequestWitness(gomock.Any(), gomock.Any()).
		DoAndReturn(func(wpr []wit.WitnessPageRequest, ch chan *wit.Response) (*wit.Request, error) {
			go func() {
				// Send response with empty data (simulates corrupted or empty witness pages).
				ch <- &wit.Response{
					Res: &wit.WitnessPacketRLPPacket{
						WitnessPacketResponse: []wit.WitnessPageResponse{{
							Page:       0,
							TotalPages: 1,
							Hash:       hashToRequest,
							Data:       []byte{}, // Empty data
						}},
					},
					Done: make(chan error, 1),
				}
			}()
			return &wit.Request{}, nil
		}).
		Times(1)

	// Call RequestWitnesses.
	req, err := p.RequestWitnesses([]common.Hash{hashToRequest}, dlCh)
	assert.NoError(t, err)
	assert.NotNil(t, req)

	// Wait for response.
	select {
	case response := <-dlCh:
		assert.NotNil(t, response)
		witnesses, ok := response.Res.([]*stateless.Witness)
		assert.True(t, ok, "should receive []*stateless.Witness type")
		assert.Equal(t, 0, len(witnesses), "should receive empty witness slice when no valid witnesses reconstructed")

	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for response")
	}
}

// TestRequestWitnesses_PartialFailureNoPanic tests that when some witness pages fail
// to be received or reconstructed, the function handles it gracefully.
func TestRequestWitnesses_PartialFailureNoPanic(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	hashToRequest := common.Hash{123}
	mockWitPeer := NewMockWitnessPeer(ctrl)
	p := &ethPeer{
		Peer:    eth.NewPeer(1, p2p.NewPeer(enode.ID{0x01, 0x02}, "test-peer", []p2p.Cap{}), nil, nil),
		witPeer: &witPeer{Peer: mockWitPeer},
	}
	dlCh := make(chan *eth.Response, 1)

	mockWitPeer.EXPECT().Log().Return(log.New()).AnyTimes()

	// Mock RequestWitness to send malformed response (wrong type).
	mockWitPeer.
		EXPECT().
		RequestWitness(gomock.Any(), gomock.Any()).
		DoAndReturn(func(wpr []wit.WitnessPageRequest, ch chan *wit.Response) (*wit.Request, error) {
			go func() {
				// Send response with wrong type (not WitnessPacketRLPPacket).
				ch <- &wit.Response{
					Res:  "invalid_response_type", // Wrong type
					Done: make(chan error, 1),
				}
			}()
			return &wit.Request{}, nil
		}).
		AnyTimes()

	// Call RequestWitnesses.
	req, err := p.RequestWitnesses([]common.Hash{hashToRequest}, dlCh)
	assert.NoError(t, err)
	assert.NotNil(t, req)

	// Wait for response - should get empty response due to processing failure.
	select {
	case response := <-dlCh:
		assert.NotNil(t, response)
		witnesses, ok := response.Res.([]*stateless.Witness)
		assert.True(t, ok, "should receive []*stateless.Witness type even on processing failure")
		assert.Equal(t, 0, len(witnesses), "should receive empty witness slice when processing fails")

	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for response")
	}
}

// TestRequestWitnessPageCount_WIT1Protocol tests the new metadata request method
func TestRequestWitnessPageCount_WIT1Protocol(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	hash := common.Hash{0xab, 0xcd}
	expectedPageCount := uint64(15)

	mockWitPeer := NewMockWitnessPeer(ctrl)
	mockWitPeer.EXPECT().Version().Return(uint(wit.WIT1)).AnyTimes()
	mockWitPeer.EXPECT().Log().Return(log.New()).AnyTimes()

	p := &ethPeer{
		Peer:    eth.NewPeer(1, p2p.NewPeer(enode.ID{0x01}, "test", []p2p.Cap{}), nil, nil),
		witPeer: &witPeer{Peer: mockWitPeer},
	}

	// Mock RequestWitnessMetadata to return page count
	mockWitPeer.EXPECT().
		RequestWitnessMetadata(gomock.Eq([]common.Hash{hash}), gomock.Any()).
		DoAndReturn(func(hashes []common.Hash, ch chan *wit.Response) (*wit.Request, error) {
			go func() {
				ch <- &wit.Response{
					Res: &wit.WitnessMetadataPacket{
						Metadata: []wit.WitnessMetadataResponse{{
							Hash:        hash,
							TotalPages:  expectedPageCount,
							WitnessSize: 225 * 1024 * 1024,
							BlockNumber: 100,
							Available:   true,
						}},
					},
				}
			}()
			return &wit.Request{}, nil
		})

	pageCount, err := p.RequestWitnessPageCount(hash)

	assert.NoError(t, err)
	assert.Equal(t, expectedPageCount, pageCount)
}

// TestRequestWitnessPageCount_WIT0FallbackToLegacy tests fallback to legacy method
func TestRequestWitnessPageCount_WIT0FallbackToLegacy(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	hash := common.Hash{0xab, 0xcd}
	expectedPageCount := uint64(10)

	mockWitPeer := NewMockWitnessPeer(ctrl)
	mockWitPeer.EXPECT().Version().Return(uint(wit.WIT0)).AnyTimes()
	mockWitPeer.EXPECT().Log().Return(log.New()).AnyTimes()

	p := &ethPeer{
		Peer:    eth.NewPeer(1, p2p.NewPeer(enode.ID{0x01}, "test", []p2p.Cap{}), nil, nil),
		witPeer: &witPeer{Peer: mockWitPeer},
	}

	// Mock RequestWitness (legacy) to return first page with TotalPages
	mockWitPeer.EXPECT().
		RequestWitness(gomock.Eq([]wit.WitnessPageRequest{{Hash: hash, Page: 0}}), gomock.Any()).
		DoAndReturn(func(wpr []wit.WitnessPageRequest, ch chan *wit.Response) (*wit.Request, error) {
			go func() {
				ch <- &wit.Response{
					Res: &wit.WitnessPacketRLPPacket{
						WitnessPacketResponse: []wit.WitnessPageResponse{{
							Page:       0,
							TotalPages: expectedPageCount,
							Hash:       hash,
							Data:       []byte{0x01, 0x02},
						}},
					},
				}
			}()
			return &wit.Request{}, nil
		})

	pageCount, err := p.RequestWitnessPageCount(hash)

	assert.NoError(t, err)
	assert.Equal(t, expectedPageCount, pageCount)
}

// TestRequestWitnessPageCount_NoWitPeer tests error when witness peer not available
func TestRequestWitnessPageCount_NoWitPeer(t *testing.T) {
	p := &ethPeer{} // no witPeer set

	pageCount, err := p.RequestWitnessPageCount(common.Hash{0x01})

	assert.Error(t, err)
	assert.Equal(t, uint64(0), pageCount)
	assert.Contains(t, err.Error(), "witness peer not found")
}

// TestRequestWitnessPageCount_MetadataNotAvailable tests unavailable witness
func TestRequestWitnessPageCount_MetadataNotAvailable(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	hash := common.Hash{0xab, 0xcd}

	mockWitPeer := NewMockWitnessPeer(ctrl)
	mockWitPeer.EXPECT().Version().Return(uint(wit.WIT1)).AnyTimes()
	mockWitPeer.EXPECT().Log().Return(log.New()).AnyTimes()

	p := &ethPeer{
		Peer:    eth.NewPeer(1, p2p.NewPeer(enode.ID{0x01}, "test", []p2p.Cap{}), nil, nil),
		witPeer: &witPeer{Peer: mockWitPeer},
	}

	// Mock RequestWitnessMetadata to return unavailable witness
	mockWitPeer.EXPECT().
		RequestWitnessMetadata(gomock.Any(), gomock.Any()).
		DoAndReturn(func(hashes []common.Hash, ch chan *wit.Response) (*wit.Request, error) {
			go func() {
				ch <- &wit.Response{
					Res: &wit.WitnessMetadataPacket{
						Metadata: []wit.WitnessMetadataResponse{{
							Hash:      hash,
							Available: false, // Not available
						}},
					},
				}
			}()
			return &wit.Request{}, nil
		})

	pageCount, err := p.RequestWitnessPageCount(hash)

	assert.Error(t, err)
	assert.Equal(t, uint64(0), pageCount)
	assert.Contains(t, err.Error(), "witness not available")
}

// TestRequestWitnessPageCount_EmptyMetadataResponse tests empty metadata response
func TestRequestWitnessPageCount_EmptyMetadataResponse(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	hash := common.Hash{0xab, 0xcd}

	mockWitPeer := NewMockWitnessPeer(ctrl)
	mockWitPeer.EXPECT().Version().Return(uint(wit.WIT1)).AnyTimes()
	mockWitPeer.EXPECT().Log().Return(log.New()).AnyTimes()

	p := &ethPeer{
		Peer:    eth.NewPeer(1, p2p.NewPeer(enode.ID{0x01}, "test", []p2p.Cap{}), nil, nil),
		witPeer: &witPeer{Peer: mockWitPeer},
	}

	// Mock RequestWitnessMetadata to return empty metadata
	mockWitPeer.EXPECT().
		RequestWitnessMetadata(gomock.Any(), gomock.Any()).
		DoAndReturn(func(hashes []common.Hash, ch chan *wit.Response) (*wit.Request, error) {
			go func() {
				ch <- &wit.Response{
					Res: &wit.WitnessMetadataPacket{
						Metadata: []wit.WitnessMetadataResponse{}, // Empty
					},
				}
			}()
			return &wit.Request{}, nil
		})

	pageCount, err := p.RequestWitnessPageCount(hash)

	assert.Error(t, err)
	assert.Equal(t, uint64(0), pageCount)
	assert.Contains(t, err.Error(), "empty witness metadata response")
}

// TestRequestWitnessPageCount_WrongResponseType tests wrong response type handling
func TestRequestWitnessPageCount_WrongResponseType(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	hash := common.Hash{0xab, 0xcd}

	mockWitPeer := NewMockWitnessPeer(ctrl)
	mockWitPeer.EXPECT().Version().Return(uint(wit.WIT1)).AnyTimes()
	mockWitPeer.EXPECT().Log().Return(log.New()).AnyTimes()

	p := &ethPeer{
		Peer:    eth.NewPeer(1, p2p.NewPeer(enode.ID{0x01}, "test", []p2p.Cap{}), nil, nil),
		witPeer: &witPeer{Peer: mockWitPeer},
	}

	// Mock RequestWitnessMetadata to return wrong type
	mockWitPeer.EXPECT().
		RequestWitnessMetadata(gomock.Any(), gomock.Any()).
		DoAndReturn(func(hashes []common.Hash, ch chan *wit.Response) (*wit.Request, error) {
			go func() {
				ch <- &wit.Response{
					Res: "wrong_type", // Wrong type
				}
			}()
			return &wit.Request{}, nil
		})

	pageCount, err := p.RequestWitnessPageCount(hash)

	assert.Error(t, err)
	assert.Equal(t, uint64(0), pageCount)
	assert.Contains(t, err.Error(), "unexpected witness metadata response type")
}

// TestRequestWitnessPageCount_Timeout tests timeout handling
func TestRequestWitnessPageCount_Timeout(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	hash := common.Hash{0xab, 0xcd}

	mockWitPeer := NewMockWitnessPeer(ctrl)
	mockWitPeer.EXPECT().Version().Return(uint(wit.WIT1)).AnyTimes()
	mockWitPeer.EXPECT().Log().Return(log.New()).AnyTimes()

	p := &ethPeer{
		Peer:    eth.NewPeer(1, p2p.NewPeer(enode.ID{0x01}, "test", []p2p.Cap{}), nil, nil),
		witPeer: &witPeer{Peer: mockWitPeer},
	}

	// Mock RequestWitnessMetadata to never respond (timeout scenario)
	mockWitPeer.EXPECT().
		RequestWitnessMetadata(gomock.Any(), gomock.Any()).
		DoAndReturn(func(hashes []common.Hash, ch chan *wit.Response) (*wit.Request, error) {
			// Don't send any response - will trigger timeout
			return &wit.Request{}, nil
		})

	pageCount, err := p.RequestWitnessPageCount(hash)

	assert.Error(t, err)
	assert.Equal(t, uint64(0), pageCount)
	assert.Contains(t, err.Error(), "timeout")
}

// TestRequestWitnessPageCount_NilResponse tests nil response handling
func TestRequestWitnessPageCount_NilResponse(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	hash := common.Hash{0xab, 0xcd}

	mockWitPeer := NewMockWitnessPeer(ctrl)
	mockWitPeer.EXPECT().Version().Return(uint(wit.WIT1)).AnyTimes()
	mockWitPeer.EXPECT().Log().Return(log.New()).AnyTimes()

	p := &ethPeer{
		Peer:    eth.NewPeer(1, p2p.NewPeer(enode.ID{0x01}, "test", []p2p.Cap{}), nil, nil),
		witPeer: &witPeer{Peer: mockWitPeer},
	}

	// Mock RequestWitnessMetadata to return nil response
	mockWitPeer.EXPECT().
		RequestWitnessMetadata(gomock.Any(), gomock.Any()).
		DoAndReturn(func(hashes []common.Hash, ch chan *wit.Response) (*wit.Request, error) {
			go func() {
				ch <- nil // Nil response
			}()
			return &wit.Request{}, nil
		})

	pageCount, err := p.RequestWitnessPageCount(hash)

	assert.Error(t, err)
	assert.Equal(t, uint64(0), pageCount)
	assert.Contains(t, err.Error(), "nil witness metadata response")
}

// TestSupportsWitness tests the SupportsWitness method
func TestSupportsWitness(t *testing.T) {
	t.Run("WithWitPeer", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockWitPeer := NewMockWitnessPeer(ctrl)
		p := &ethPeer{
			Peer:    eth.NewPeer(1, p2p.NewPeer(enode.ID{0x01}, "test", []p2p.Cap{}), nil, nil),
			witPeer: &witPeer{Peer: mockWitPeer},
		}

		assert.True(t, p.SupportsWitness())
	})

	t.Run("WithoutWitPeer", func(t *testing.T) {
		p := &ethPeer{
			Peer: eth.NewPeer(1, p2p.NewPeer(enode.ID{0x01}, "test", []p2p.Cap{}), nil, nil),
		}

		assert.False(t, p.SupportsWitness())
	})
}

// TestReconstructWitness tests witness reconstruction from pages
func TestReconstructWitness(t *testing.T) {
	t.Run("SuccessfulReconstruction", func(t *testing.T) {
		// Create a test witness and encode it
		witness, _ := stateless.NewWitness(&types.Header{Number: big.NewInt(100)}, nil)
		FillWitnessWithDeterministicRandomState(witness, 5*1024)
		var buf bytes.Buffer
		witness.EncodeRLP(&buf)
		witnessBytes := buf.Bytes()

		// Split into pages
		pageSize := 1024
		var pages []wit.WitnessPageResponse
		for i := 0; i < len(witnessBytes); i += pageSize {
			end := i + pageSize
			if end > len(witnessBytes) {
				end = len(witnessBytes)
			}
			pages = append(pages, wit.WitnessPageResponse{
				Page:       uint64(len(pages)),
				TotalPages: uint64((len(witnessBytes) + pageSize - 1) / pageSize),
				Hash:       common.Hash{0x01},
				Data:       witnessBytes[i:end],
			})
		}

		// Reconstruct
		p := &ethPeer{Peer: eth.NewPeer(1, p2p.NewPeer(enode.ID{0x01}, "test", []p2p.Cap{}), nil, nil)}
		reconstructed, err := p.reconstructWitness(pages)

		assert.NoError(t, err)
		assert.NotNil(t, reconstructed)
		assert.Equal(t, witness.Header().Number.Uint64(), reconstructed.Header().Number.Uint64())
	})

	t.Run("OutOfOrderPages", func(t *testing.T) {
		// Create pages out of order
		witness, _ := stateless.NewWitness(&types.Header{Number: big.NewInt(100)}, nil)
		FillWitnessWithDeterministicRandomState(witness, 3*1024)
		var buf bytes.Buffer
		witness.EncodeRLP(&buf)
		witnessBytes := buf.Bytes()

		pageSize := 1024
		var pages []wit.WitnessPageResponse
		for i := 0; i < len(witnessBytes); i += pageSize {
			end := i + pageSize
			if end > len(witnessBytes) {
				end = len(witnessBytes)
			}
			pages = append(pages, wit.WitnessPageResponse{
				Page:       uint64(len(pages)),
				TotalPages: uint64((len(witnessBytes) + pageSize - 1) / pageSize),
				Hash:       common.Hash{0x01},
				Data:       witnessBytes[i:end],
			})
		}

		// Shuffle pages
		pages[0], pages[2] = pages[2], pages[0]

		// Reconstruct - should still work due to sorting
		p := &ethPeer{Peer: eth.NewPeer(1, p2p.NewPeer(enode.ID{0x01}, "test", []p2p.Cap{}), nil, nil)}
		reconstructed, err := p.reconstructWitness(pages)

		assert.NoError(t, err)
		assert.NotNil(t, reconstructed)
	})

	t.Run("InvalidRLPData", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		// Create pages with invalid RLP data
		pages := []wit.WitnessPageResponse{
			{
				Page:       0,
				TotalPages: 1,
				Hash:       common.Hash{0x01},
				Data:       []byte{0xFF, 0xFF, 0xFF}, // Invalid RLP
			},
		}

		mockWitPeer := NewMockWitnessPeer(ctrl)
		mockWitPeer.EXPECT().Log().Return(log.New()).AnyTimes()

		p := &ethPeer{
			Peer:    eth.NewPeer(1, p2p.NewPeer(enode.ID{0x01}, "test", []p2p.Cap{}), nil, nil),
			witPeer: &witPeer{Peer: mockWitPeer},
		}
		reconstructed, err := p.reconstructWitness(pages)

		assert.Error(t, err)
		assert.Nil(t, reconstructed)
	})
}

// TestEthWitRequestClose tests the Close method of ethWitRequest
func TestEthWitRequestClose(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create mock wit requests
	mockWitReq1 := &wit.Request{}
	mockWitReq2 := &wit.Request{}

	ethReq := &eth.Request{
		Peer:   "test-peer",
		Cancel: make(chan struct{}),
	}

	witReq := &ethWitRequest{
		Request: ethReq,
		witReqs: []*wit.Request{mockWitReq1, mockWitReq2},
	}

	// Close should not error
	err := witReq.Close()
	assert.NoError(t, err)

	// Verify cancel channel was closed
	select {
	case <-ethReq.Cancel:
		// Expected - channel was closed
	default:
		t.Error("Cancel channel was not closed")
	}
}

// TestJailPeerForViolation tests the jailPeerForViolation helper method
func TestJailPeerForViolation(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockWitPeer := NewMockWitnessPeer(ctrl)
	mockWitPeer.EXPECT().Log().Return(log.New()).AnyTimes()

	p := &ethPeer{
		Peer:    eth.NewPeer(1, p2p.NewPeer(enode.ID{0x01}, "test-peer", []p2p.Cap{}), nil, nil),
		witPeer: &witPeer{Peer: mockWitPeer},
	}

	t.Run("WithJailCallback", func(t *testing.T) {
		jailCalled := false
		jailedPeerID := ""
		jailPeer := func(id string) {
			jailCalled = true
			jailedPeerID = id
		}

		err := p.jailPeerForViolation(jailPeer, "invalid page number", map[string]interface{}{
			"page":       uint64(10),
			"totalPages": uint64(5),
			"hash":       common.Hash{0xaa},
		})

		assert.Error(t, err)
		assert.True(t, jailCalled, "jail callback should have been called")
		assert.Equal(t, p.ID(), jailedPeerID)
		assert.Contains(t, err.Error(), "invalid page number")
	})

	t.Run("WithoutJailCallback", func(t *testing.T) {
		err := p.jailPeerForViolation(nil, "inconsistent TotalPages", map[string]interface{}{
			"existing": uint64(10),
			"new":      uint64(20),
		})

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "inconsistent TotalPages")
	})

	t.Run("MultipleDetails", func(t *testing.T) {
		err := p.jailPeerForViolation(nil, "test violation", map[string]interface{}{
			"field1": "value1",
			"field2": 123,
			"field3": true,
		})

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "test violation")
		// Error should contain all details in some form
	})

	t.Run("EmptyDetails", func(t *testing.T) {
		err := p.jailPeerForViolation(nil, "simple violation", map[string]interface{}{})

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "simple violation")
	})
}

// TestRequestWitnessPageCount_ErrorRequestingMetadata tests error path when RequestWitnessMetadata fails
func TestRequestWitnessPageCount_ErrorRequestingMetadata(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	hash := common.Hash{0xab, 0xcd}
	expectedError := errors.New("network error")

	mockWitPeer := NewMockWitnessPeer(ctrl)
	mockWitPeer.EXPECT().Version().Return(uint(wit.WIT1)).AnyTimes()
	mockWitPeer.EXPECT().Log().Return(log.New()).AnyTimes()
	mockWitPeer.EXPECT().ID().Return("test-peer").AnyTimes()

	p := &ethPeer{
		Peer:    eth.NewPeer(1, p2p.NewPeer(enode.ID{0x01}, "test", []p2p.Cap{}), nil, nil),
		witPeer: &witPeer{Peer: mockWitPeer},
	}

	// Mock RequestWitnessMetadata to return error
	mockWitPeer.EXPECT().
		RequestWitnessMetadata(gomock.Eq([]common.Hash{hash}), gomock.Any()).
		Return(nil, expectedError)

	pageCount, err := p.RequestWitnessPageCount(hash)

	assert.Error(t, err)
	assert.Equal(t, expectedError, err)
	assert.Equal(t, uint64(0), pageCount)
}

// TestRequestWitnessPageCountLegacy_ErrorRequesting tests error path when RequestWitness fails in legacy method
func TestRequestWitnessPageCountLegacy_ErrorRequesting(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	hash := common.Hash{0xab, 0xcd}
	expectedError := errors.New("request failed")

	mockWitPeer := NewMockWitnessPeer(ctrl)
	mockWitPeer.EXPECT().Version().Return(uint(wit.WIT0)).AnyTimes()
	mockWitPeer.EXPECT().Log().Return(log.New()).AnyTimes()
	mockWitPeer.EXPECT().ID().Return("test-peer").AnyTimes()

	p := &ethPeer{
		Peer:    eth.NewPeer(1, p2p.NewPeer(enode.ID{0x01}, "test", []p2p.Cap{}), nil, nil),
		witPeer: &witPeer{Peer: mockWitPeer},
	}

	// Mock RequestWitness to return error
	mockWitPeer.EXPECT().
		RequestWitness(gomock.Eq([]wit.WitnessPageRequest{{Hash: hash, Page: 0}}), gomock.Any()).
		Return(nil, expectedError)

	pageCount, err := p.RequestWitnessPageCount(hash)

	assert.Error(t, err)
	assert.Equal(t, expectedError, err)
	assert.Equal(t, uint64(0), pageCount)
}

// TestRequestWitnessPageCountLegacy_NilResponse tests nil response handling in legacy method
func TestRequestWitnessPageCountLegacy_NilResponse(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	hash := common.Hash{0xab, 0xcd}

	mockWitPeer := NewMockWitnessPeer(ctrl)
	mockWitPeer.EXPECT().Version().Return(uint(wit.WIT0)).AnyTimes()
	mockWitPeer.EXPECT().Log().Return(log.New()).AnyTimes()
	mockWitPeer.EXPECT().ID().Return("test-peer").AnyTimes()

	p := &ethPeer{
		Peer:    eth.NewPeer(1, p2p.NewPeer(enode.ID{0x01}, "test", []p2p.Cap{}), nil, nil),
		witPeer: &witPeer{Peer: mockWitPeer},
	}

	// Mock RequestWitness to return nil response
	mockWitPeer.EXPECT().
		RequestWitness(gomock.Eq([]wit.WitnessPageRequest{{Hash: hash, Page: 0}}), gomock.Any()).
		DoAndReturn(func(wpr []wit.WitnessPageRequest, ch chan *wit.Response) (*wit.Request, error) {
			go func() {
				ch <- nil // Nil response
			}()
			return &wit.Request{}, nil
		})

	pageCount, err := p.RequestWitnessPageCount(hash)

	assert.Error(t, err)
	assert.Equal(t, uint64(0), pageCount)
	assert.Contains(t, err.Error(), "nil witness response")
}

// TestRequestWitnessPageCountLegacy_WrongResponseType tests unexpected response type in legacy method
func TestRequestWitnessPageCountLegacy_WrongResponseType(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	hash := common.Hash{0xab, 0xcd}

	mockWitPeer := NewMockWitnessPeer(ctrl)
	mockWitPeer.EXPECT().Version().Return(uint(wit.WIT0)).AnyTimes()
	mockWitPeer.EXPECT().Log().Return(log.New()).AnyTimes()
	mockWitPeer.EXPECT().ID().Return("test-peer").AnyTimes()

	p := &ethPeer{
		Peer:    eth.NewPeer(1, p2p.NewPeer(enode.ID{0x01}, "test", []p2p.Cap{}), nil, nil),
		witPeer: &witPeer{Peer: mockWitPeer},
	}

	// Mock RequestWitness to return wrong type
	mockWitPeer.EXPECT().
		RequestWitness(gomock.Eq([]wit.WitnessPageRequest{{Hash: hash, Page: 0}}), gomock.Any()).
		DoAndReturn(func(wpr []wit.WitnessPageRequest, ch chan *wit.Response) (*wit.Request, error) {
			go func() {
				ch <- &wit.Response{
					Res: "wrong_type", // Wrong type, not *wit.WitnessPacketRLPPacket
				}
			}()
			return &wit.Request{}, nil
		})

	pageCount, err := p.RequestWitnessPageCount(hash)

	assert.Error(t, err)
	assert.Equal(t, uint64(0), pageCount)
	assert.Contains(t, err.Error(), "unexpected witness response type")
}

// TestRequestWitnessPageCountLegacy_EmptyPacketResponse tests empty witness packet response in legacy method
func TestRequestWitnessPageCountLegacy_EmptyPacketResponse(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	hash := common.Hash{0xab, 0xcd}

	mockWitPeer := NewMockWitnessPeer(ctrl)
	mockWitPeer.EXPECT().Version().Return(uint(wit.WIT0)).AnyTimes()
	mockWitPeer.EXPECT().Log().Return(log.New()).AnyTimes()
	mockWitPeer.EXPECT().ID().Return("test-peer").AnyTimes()

	p := &ethPeer{
		Peer:    eth.NewPeer(1, p2p.NewPeer(enode.ID{0x01}, "test", []p2p.Cap{}), nil, nil),
		witPeer: &witPeer{Peer: mockWitPeer},
	}

	// Mock RequestWitness to return empty packet response
	mockWitPeer.EXPECT().
		RequestWitness(gomock.Eq([]wit.WitnessPageRequest{{Hash: hash, Page: 0}}), gomock.Any()).
		DoAndReturn(func(wpr []wit.WitnessPageRequest, ch chan *wit.Response) (*wit.Request, error) {
			go func() {
				ch <- &wit.Response{
					Res: &wit.WitnessPacketRLPPacket{
						WitnessPacketResponse: []wit.WitnessPageResponse{}, // Empty
					},
				}
			}()
			return &wit.Request{}, nil
		})

	pageCount, err := p.RequestWitnessPageCount(hash)

	assert.Error(t, err)
	assert.Equal(t, uint64(0), pageCount)
	assert.Contains(t, err.Error(), "empty witness packet response")
}

// TestRequestWitnessPageCountLegacy_Timeout tests timeout in legacy method
func TestRequestWitnessPageCountLegacy_Timeout(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	hash := common.Hash{0xab, 0xcd}

	mockWitPeer := NewMockWitnessPeer(ctrl)
	mockWitPeer.EXPECT().Version().Return(uint(wit.WIT0)).AnyTimes()
	mockWitPeer.EXPECT().Log().Return(log.New()).AnyTimes()
	mockWitPeer.EXPECT().ID().Return("test-peer").AnyTimes()

	p := &ethPeer{
		Peer:    eth.NewPeer(1, p2p.NewPeer(enode.ID{0x01}, "test", []p2p.Cap{}), nil, nil),
		witPeer: &witPeer{Peer: mockWitPeer},
	}

	// Mock RequestWitness to never respond (timeout scenario)
	mockWitPeer.EXPECT().
		RequestWitness(gomock.Eq([]wit.WitnessPageRequest{{Hash: hash, Page: 0}}), gomock.Any()).
		DoAndReturn(func(wpr []wit.WitnessPageRequest, ch chan *wit.Response) (*wit.Request, error) {
			// Don't send any response - will trigger timeout
			return &wit.Request{}, nil
		})

	pageCount, err := p.RequestWitnessPageCount(hash)

	assert.Error(t, err)
	assert.Equal(t, uint64(0), pageCount)
	assert.Contains(t, err.Error(), "timeout waiting for witness page count from peer")
}

// TestRequestWitnessesWithVerification_InvalidPageNumber tests invalid page number validation
func TestRequestWitnessesWithVerification_InvalidPageNumber(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	hash := common.Hash{0xab, 0xcd}
	jailCalled := false
	jailedPeerID := ""

	mockWitPeer := NewMockWitnessPeer(ctrl)
	mockWitPeer.EXPECT().Log().Return(log.New()).AnyTimes()
	mockWitPeer.EXPECT().ID().Return("test-peer").AnyTimes()

	p := &ethPeer{
		Peer:    eth.NewPeer(1, p2p.NewPeer(enode.ID{0x01}, "test-peer", []p2p.Cap{}), nil, nil),
		witPeer: &witPeer{Peer: mockWitPeer},
	}
	dlCh := make(chan *eth.Response, 1)

	jailPeer := func(id string) {
		jailCalled = true
		jailedPeerID = id
	}

	// Mock RequestWitness to return page with invalid page number (page >= TotalPages)
	mockWitPeer.EXPECT().
		RequestWitness(gomock.Any(), gomock.Any()).
		DoAndReturn(func(wpr []wit.WitnessPageRequest, ch chan *wit.Response) (*wit.Request, error) {
			go func() {
				// Send page with Page=5 but TotalPages=5 (invalid: page >= totalPages)
				ch <- &wit.Response{
					Res: &wit.WitnessPacketRLPPacket{
						WitnessPacketResponse: []wit.WitnessPageResponse{{
							Page:       5, // Invalid: >= TotalPages
							TotalPages: 5,
							Hash:       hash,
							Data:       []byte{0x01, 0x02},
						}},
					},
					Done: make(chan error, 1),
				}
			}()
			return &wit.Request{}, nil
		}).
		AnyTimes()

	req, err := p.RequestWitnessesWithVerification([]common.Hash{hash}, dlCh, nil, jailPeer)
	assert.NoError(t, err)
	assert.NotNil(t, req)

	// Wait for response - should get empty response due to validation failure
	select {
	case response := <-dlCh:
		assert.NotNil(t, response)
		witnesses, ok := response.Res.([]*stateless.Witness)
		assert.True(t, ok)
		assert.Equal(t, 0, len(witnesses), "should receive empty witness slice when validation fails")
		assert.True(t, jailCalled, "jail callback should have been called")
		assert.Equal(t, p.ID(), jailedPeerID, "jailed peer ID should match actual peer ID")

	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for response")
	}
}

// TestRequestWitnessesWithVerification_InconsistentTotalPages tests inconsistent TotalPages validation
func TestRequestWitnessesWithVerification_InconsistentTotalPages(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	hash := common.Hash{0xab, 0xcd}
	jailCalled := false

	mockWitPeer := NewMockWitnessPeer(ctrl)
	mockWitPeer.EXPECT().Log().Return(log.New()).AnyTimes()
	mockWitPeer.EXPECT().ID().Return("test-peer").AnyTimes()

	p := &ethPeer{
		Peer:    eth.NewPeer(1, p2p.NewPeer(enode.ID{0x01}, "test-peer", []p2p.Cap{}), nil, nil),
		witPeer: &witPeer{Peer: mockWitPeer},
	}
	dlCh := make(chan *eth.Response, 1)

	jailPeer := func(id string) {
		jailCalled = true
	}

	callCount := 0
	// Mock RequestWitness to return pages with inconsistent TotalPages
	mockWitPeer.EXPECT().
		RequestWitness(gomock.Any(), gomock.Any()).
		DoAndReturn(func(wpr []wit.WitnessPageRequest, ch chan *wit.Response) (*wit.Request, error) {
			go func() {
				callCount++
				if callCount == 1 {
					// First page: TotalPages = 10
					ch <- &wit.Response{
						Res: &wit.WitnessPacketRLPPacket{
							WitnessPacketResponse: []wit.WitnessPageResponse{{
								Page:       0,
								TotalPages: 10,
								Hash:       hash,
								Data:       []byte{0x01, 0x02},
							}},
						},
						Done: make(chan error, 1),
					}
				} else {
					// Second page: TotalPages = 20 (inconsistent!)
					ch <- &wit.Response{
						Res: &wit.WitnessPacketRLPPacket{
							WitnessPacketResponse: []wit.WitnessPageResponse{{
								Page:       1,
								TotalPages: 20, // Inconsistent with first page
								Hash:       hash,
								Data:       []byte{0x03, 0x04},
							}},
						},
						Done: make(chan error, 1),
					}
				}
			}()
			return &wit.Request{}, nil
		}).
		AnyTimes()

	req, err := p.RequestWitnessesWithVerification([]common.Hash{hash}, dlCh, nil, jailPeer)
	assert.NoError(t, err)
	assert.NotNil(t, req)

	// Wait for response
	select {
	case response := <-dlCh:
		assert.NotNil(t, response)
		witnesses, ok := response.Res.([]*stateless.Witness)
		assert.True(t, ok)
		// Should get empty response due to inconsistent TotalPages
		assert.Equal(t, 0, len(witnesses))
		assert.True(t, jailCalled, "jail callback should have been called for inconsistent TotalPages")

	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for response")
	}
}

// TestRequestWitnessesWithVerification_PeerFailedVerification tests peer verification failure
func TestRequestWitnessesWithVerification_PeerFailedVerification(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	hash := common.Hash{0xab, 0xcd}

	mockWitPeer := NewMockWitnessPeer(ctrl)
	mockWitPeer.EXPECT().Log().Return(log.New()).AnyTimes()
	mockWitPeer.EXPECT().ID().Return("test-peer").AnyTimes()

	p := &ethPeer{
		Peer:    eth.NewPeer(1, p2p.NewPeer(enode.ID{0x01}, "test-peer", []p2p.Cap{}), nil, nil),
		witPeer: &witPeer{Peer: mockWitPeer},
	}
	dlCh := make(chan *eth.Response, 1)

	// Verification function that returns false (peer is dishonest)
	verifyPageCount := func(h common.Hash, totalPages uint64, peerID string) bool {
		return false // Peer failed verification
	}

	// Mock RequestWitness to return page
	mockWitPeer.EXPECT().
		RequestWitness(gomock.Any(), gomock.Any()).
		DoAndReturn(func(wpr []wit.WitnessPageRequest, ch chan *wit.Response) (*wit.Request, error) {
			go func() {
				ch <- &wit.Response{
					Res: &wit.WitnessPacketRLPPacket{
						WitnessPacketResponse: []wit.WitnessPageResponse{{
							Page:       0,
							TotalPages: 5,
							Hash:       hash,
							Data:       []byte{0x01, 0x02},
						}},
					},
					Done: make(chan error, 1),
				}
			}()
			return &wit.Request{}, nil
		}).
		AnyTimes()

	req, err := p.RequestWitnessesWithVerification([]common.Hash{hash}, dlCh, verifyPageCount, nil)
	assert.NoError(t, err)
	assert.NotNil(t, req)

	// Wait for response - should get empty response due to verification failure
	select {
	case response := <-dlCh:
		assert.NotNil(t, response)
		witnesses, ok := response.Res.([]*stateless.Witness)
		assert.True(t, ok)
		assert.Equal(t, 0, len(witnesses), "should receive empty witness slice when verification fails")

	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for response")
	}
}

// TestRequestWitnessesWithVerification_MorePagesThanTotalPages tests receiving more pages than TotalPages
func TestRequestWitnessesWithVerification_MorePagesThanTotalPages(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	hash := common.Hash{0xab, 0xcd}
	jailCalled := false

	mockWitPeer := NewMockWitnessPeer(ctrl)
	mockWitPeer.EXPECT().Log().Return(log.New()).AnyTimes()
	mockWitPeer.EXPECT().ID().Return("test-peer").AnyTimes()

	p := &ethPeer{
		Peer:    eth.NewPeer(1, p2p.NewPeer(enode.ID{0x01}, "test-peer", []p2p.Cap{}), nil, nil),
		witPeer: &witPeer{Peer: mockWitPeer},
	}
	dlCh := make(chan *eth.Response, 1)

	jailPeer := func(id string) {
		jailCalled = true
	}

	callCount := 0
	// Mock RequestWitness to return pages that exceed TotalPages
	// First page sets TotalPages=2, then we send 3 pages total to trigger the check
	mockWitPeer.EXPECT().
		RequestWitness(gomock.Any(), gomock.Any()).
		DoAndReturn(func(wpr []wit.WitnessPageRequest, ch chan *wit.Response) (*wit.Request, error) {
			go func() {
				callCount++
				// Send pages: first sets TotalPages=2, then we send page 0, 1, and 2 (3 pages total)
				// This will trigger len(receivedWitPages) > currentTotalPages check
				ch <- &wit.Response{
					Res: &wit.WitnessPacketRLPPacket{
						WitnessPacketResponse: []wit.WitnessPageResponse{{
							Page:       uint64(callCount - 1),
							TotalPages: 2, // Only 2 pages allowed, but we'll send 3
							Hash:       hash,
							Data:       []byte{0x01, 0x02},
						}},
					},
					Done: make(chan error, 1),
				}
			}()
			return &wit.Request{}, nil
		}).
		AnyTimes()

	req, err := p.RequestWitnessesWithVerification([]common.Hash{hash}, dlCh, nil, jailPeer)
	assert.NoError(t, err)
	assert.NotNil(t, req)

	// Wait for response
	select {
	case response := <-dlCh:
		assert.NotNil(t, response)
		witnesses, ok := response.Res.([]*stateless.Witness)
		assert.True(t, ok)
		// Should get empty response due to too many pages
		assert.Equal(t, 0, len(witnesses))
		assert.True(t, jailCalled, "jail callback should have been called for too many pages")

	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for response")
	}
}

// TestRequestWitnessesWithVerification_DownloadPaused tests that download is paused correctly
// When download is paused, no more requests should be built
func TestRequestWitnessesWithVerification_DownloadPaused(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	hash := common.Hash{0xab, 0xcd}

	mockWitPeer := NewMockWitnessPeer(ctrl)
	mockWitPeer.EXPECT().Log().Return(log.New()).AnyTimes()
	mockWitPeer.EXPECT().ID().Return("test-peer").AnyTimes()

	p := &ethPeer{
		Peer:    eth.NewPeer(1, p2p.NewPeer(enode.ID{0x01}, "test-peer", []p2p.Cap{}), nil, nil),
		witPeer: &witPeer{Peer: mockWitPeer},
	}
	dlCh := make(chan *eth.Response, 1)

	// Verification function that returns false to pause download
	verifyPageCount := func(h common.Hash, totalPages uint64, peerID string) bool {
		return false // This will pause the download
	}

	// Mock RequestWitness - first page triggers verification failure which pauses download
	// When download is paused, subsequent pages should return early without building more requests
	mockWitPeer.EXPECT().
		RequestWitness(gomock.Any(), gomock.Any()).
		DoAndReturn(func(wpr []wit.WitnessPageRequest, ch chan *wit.Response) (*wit.Request, error) {
			go func() {
				// First page triggers verification failure, which pauses download
				ch <- &wit.Response{
					Res: &wit.WitnessPacketRLPPacket{
						WitnessPacketResponse: []wit.WitnessPageResponse{{
							Page:       0,
							TotalPages: 5,
							Hash:       hash,
							Data:       []byte{0x01, 0x02},
						}},
					},
					Done: make(chan error, 1),
				}
				// After verification fails and download is paused, if we receive another page,
				// receiveWitnessPage should check paused and return nil early
				// This prevents building more requests
			}()
			return &wit.Request{}, nil
		}).
		AnyTimes()

	req, err := p.RequestWitnessesWithVerification([]common.Hash{hash}, dlCh, verifyPageCount, nil)
	assert.NoError(t, err)
	assert.NotNil(t, req)

	// Wait for response - should eventually get empty response since no witnesses are reconstructed
	select {
	case response := <-dlCh:
		assert.NotNil(t, response)
		witnesses, ok := response.Res.([]*stateless.Witness)
		assert.True(t, ok)
		// Should get empty response when download is paused and no witnesses are reconstructed
		assert.Equal(t, 0, len(witnesses))

	case <-time.After(5 * time.Second):
		// Give more time for the response to come through
		// The response should eventually come as empty when no witnesses are reconstructed
		t.Fatal("timed out waiting for response")
	}
}
