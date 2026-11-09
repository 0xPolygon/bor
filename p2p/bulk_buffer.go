package p2p

import (
	"bytes"
	"context"
	"sync"

	"golang.org/x/sync/semaphore"
)

const (
	bulkPeerBufferLimit = 2 * bulkMaxMessageSize
	bulkNodeBufferLimit = 128 * 1024 * 1024
)

// Each node shares one application-copy allowance across its peers, separately
// from QUIC receive windows.
type bulkBufferPool struct {
	global      *semaphore.Weighted
	writeGlobal *semaphore.Weighted
}

func newBulkBufferPool() *bulkBufferPool {
	return &bulkBufferPool{
		global:      semaphore.NewWeighted(bulkNodeBufferLimit),
		writeGlobal: semaphore.NewWeighted(bulkNodeBufferLimit / 2),
	}
}

func (p *bulkBufferPool) newBudget() *bulkBufferBudget {
	return &bulkBufferBudget{
		peer:        semaphore.NewWeighted(bulkPeerBufferLimit),
		global:      p.global,
		writePeer:   semaphore.NewWeighted(bulkPeerBufferLimit / 2),
		writeGlobal: p.writeGlobal,
	}
}

type bulkBufferBudget struct {
	peer        *semaphore.Weighted
	global      *semaphore.Weighted
	writePeer   *semaphore.Weighted
	writeGlobal *semaphore.Weighted
}

// Standalone peers have no server pool and need a private allowance.
func newBulkBufferBudget() *bulkBufferBudget {
	return newBulkBufferPool().newBudget()
}

func bulkBuffersFor(rw MsgReadWriter) *bulkBufferBudget {
	if source, ok := rw.(interface{ bulkBuffers() *bulkBufferBudget }); ok {
		if budget := source.bulkBuffers(); budget != nil {
			return budget
		}
	}
	return newBulkBufferBudget()
}

func (b *bulkBufferBudget) acquire(ctx context.Context, size uint32) error {
	if err := b.peer.Acquire(ctx, int64(size)); err != nil {
		return err
	}
	if err := b.global.Acquire(ctx, int64(size)); err != nil {
		b.peer.Release(int64(size))
		return err
	}
	return nil
}

func (b *bulkBufferBudget) tryAcquire(size uint32) bool {
	if !b.peer.TryAcquire(int64(size)) {
		return false
	}
	if !b.global.TryAcquire(int64(size)) {
		b.peer.Release(int64(size))
		return false
	}
	return true
}

func (b *bulkBufferBudget) release(size uint32) {
	b.global.Release(int64(size))
	b.peer.Release(int64(size))
}

func (b *bulkBufferBudget) tryAcquireWrite(size uint32) bool {
	// Reserve receive capacity even when writes stall on remote flow control.
	if !b.writePeer.TryAcquire(int64(size)) {
		return false
	}
	if !b.writeGlobal.TryAcquire(int64(size)) {
		b.writePeer.Release(int64(size))
		return false
	}
	if !b.tryAcquire(size) {
		b.writeGlobal.Release(int64(size))
		b.writePeer.Release(int64(size))
		return false
	}
	return true
}

func (b *bulkBufferBudget) releaseWrite(size uint32) {
	b.release(size)
	b.writeGlobal.Release(int64(size))
	b.writePeer.Release(int64(size))
}

type bulkBufferedPayload struct {
	mu     sync.Mutex
	reader bytes.Reader
	budget *bulkBufferBudget
	size   uint32
}

func (p *bulkBufferedPayload) Read(buf []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reader.Read(buf)
}

func (p *bulkBufferedPayload) release() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.budget != nil {
		// A handler may retain its Msg after shutdown. Drop the backing array
		// before returning its allowance, including when Read races with close.
		p.reader.Reset(nil)
		p.budget.release(p.size)
		p.budget = nil
	}
}
