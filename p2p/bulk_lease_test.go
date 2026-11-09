package p2p

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/p2p/enode"
)

func TestBulkLeaseBookSingleLeasePerIdentity(t *testing.T) {
	book := newBulkLeaseBook()
	id := enode.ID{0x1}

	release, ok := book.acquire(id)
	require.True(t, ok)
	require.True(t, book.holds(id))
	require.Equal(t, 1, book.active())

	_, ok = book.acquire(id)
	require.False(t, ok, "a held identity cannot take a second lease")

	// A distinct identity is independent.
	other, ok := book.acquire(enode.ID{0x2})
	require.True(t, ok)
	require.Equal(t, 2, book.active())

	release()
	release() // idempotent
	require.False(t, book.holds(id))
	require.Equal(t, 1, book.active())

	reacquire, ok := book.acquire(id)
	require.True(t, ok, "a released identity can reconnect")
	reacquire()
	other()
	require.Zero(t, book.active())
}

func TestBulkLeaseBookStaleReleaseKeepsNewerLease(t *testing.T) {
	book := newBulkLeaseBook()
	id := enode.ID{0x1}

	releaseOld, ok := book.acquire(id)
	require.True(t, ok)
	releaseOld()

	releaseNew, ok := book.acquire(id)
	require.True(t, ok)

	// The old connection's late release targets its own generation only.
	releaseOld()
	require.True(t, book.holds(id), "stale release must not revoke the newer lease")

	releaseNew()
	require.False(t, book.holds(id))
}
