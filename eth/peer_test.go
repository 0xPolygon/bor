package eth // replace with the actual package name

import (
	"bytes"
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
	witness.EncodeCompressed(&witBuf)

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
	witness.EncodeCompressed(&witBuf)

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
