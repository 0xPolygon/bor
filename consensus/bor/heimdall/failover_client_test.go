package heimdall

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/0xPolygon/heimdall-v2/x/bor/types"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/consensus/bor/clerk"
	"github.com/ethereum/go-ethereum/consensus/bor/heimdall/checkpoint"
	"github.com/ethereum/go-ethereum/consensus/bor/heimdall/milestone"
)

// mockHeimdallClient is a configurable mock implementing the heimdallClient interface.
type mockHeimdallClient struct {
	getSpanFn func(ctx context.Context, spanID uint64) (*types.Span, error)
	closeFn   func()
	hits      atomic.Int32
}

func (m *mockHeimdallClient) StateSyncEvents(_ context.Context, _ uint64, _ int64) ([]*clerk.EventRecordWithTime, error) {
	return nil, nil
}

func (m *mockHeimdallClient) GetSpan(ctx context.Context, spanID uint64) (*types.Span, error) {
	m.hits.Add(1)

	if m.getSpanFn != nil {
		return m.getSpanFn(ctx, spanID)
	}

	return &types.Span{Id: spanID}, nil
}

func (m *mockHeimdallClient) GetLatestSpan(_ context.Context) (*types.Span, error) {
	return nil, nil
}

func (m *mockHeimdallClient) FetchCheckpoint(_ context.Context, _ int64) (*checkpoint.Checkpoint, error) {
	return nil, nil
}

func (m *mockHeimdallClient) FetchCheckpointCount(_ context.Context) (int64, error) {
	return 0, nil
}

func (m *mockHeimdallClient) FetchMilestone(_ context.Context) (*milestone.Milestone, error) {
	return nil, nil
}

func (m *mockHeimdallClient) FetchMilestoneCount(_ context.Context) (int64, error) {
	return 0, nil
}

func (m *mockHeimdallClient) FetchStatus(_ context.Context) (*ctypes.SyncInfo, error) {
	return nil, nil
}

func (m *mockHeimdallClient) Close() {
	if m.closeFn != nil {
		m.closeFn()
	}
}

func TestFailover_SwitchOnPrimaryDown(t *testing.T) {
	primary := &mockHeimdallClient{
		getSpanFn: func(ctx context.Context, _ uint64) (*types.Span, error) {
			// Simulate transport error
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
		},
	}
	secondary := &mockHeimdallClient{}

	fc := NewFailoverHeimdallClient(primary, secondary)
	fc.attemptTimeout = 100 * time.Millisecond
	defer fc.Close()

	span, err := fc.GetSpan(context.Background(), 1)
	require.NoError(t, err)
	require.NotNil(t, span)

	assert.GreaterOrEqual(t, primary.hits.Load(), int32(1), "primary should have been tried")
	assert.Equal(t, int32(1), secondary.hits.Load(), "secondary should have been called once")
}

func TestFailover_NoSwitchOnContextCanceled(t *testing.T) {
	primary := &mockHeimdallClient{
		getSpanFn: func(ctx context.Context, _ uint64) (*types.Span, error) {
			// Block until context is cancelled
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	secondary := &mockHeimdallClient{}

	fc := NewFailoverHeimdallClient(primary, secondary)
	fc.attemptTimeout = 5 * time.Second // longer than caller's ctx
	defer fc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := fc.GetSpan(ctx, 1)
	require.Error(t, err)
	assert.Equal(t, int32(0), secondary.hits.Load(), "should not failover on caller context cancellation")
}

func TestFailover_NoSwitchOnServiceUnavailable(t *testing.T) {
	primary := &mockHeimdallClient{
		getSpanFn: func(_ context.Context, _ uint64) (*types.Span, error) {
			return nil, ErrServiceUnavailable
		},
	}
	secondary := &mockHeimdallClient{}

	fc := NewFailoverHeimdallClient(primary, secondary)
	fc.attemptTimeout = 100 * time.Millisecond
	defer fc.Close()

	_, err := fc.GetSpan(context.Background(), 1)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrServiceUnavailable))
	assert.Equal(t, int32(0), secondary.hits.Load(), "should not failover on 503")
}

func TestFailover_NoSwitchOnShutdownDetected(t *testing.T) {
	primary := &mockHeimdallClient{
		getSpanFn: func(_ context.Context, _ uint64) (*types.Span, error) {
			return nil, ErrShutdownDetected
		},
	}
	secondary := &mockHeimdallClient{}

	fc := NewFailoverHeimdallClient(primary, secondary)
	fc.attemptTimeout = 100 * time.Millisecond
	defer fc.Close()

	_, err := fc.GetSpan(context.Background(), 1)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrShutdownDetected))
	assert.Equal(t, int32(0), secondary.hits.Load(), "should not failover on shutdown")
}

func TestFailover_StickyBehavior(t *testing.T) {
	primary := &mockHeimdallClient{
		getSpanFn: func(_ context.Context, _ uint64) (*types.Span, error) {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
		},
	}
	secondary := &mockHeimdallClient{}

	fc := NewFailoverHeimdallClient(primary, secondary)
	fc.attemptTimeout = 100 * time.Millisecond
	fc.cooldown = 1 * time.Hour // very long cooldown
	defer fc.Close()

	// First call triggers failover
	_, err := fc.GetSpan(context.Background(), 1)
	require.NoError(t, err)

	primaryBefore := primary.hits.Load()
	secondaryBefore := secondary.hits.Load()

	// Subsequent calls should go directly to secondary without trying primary
	for i := 0; i < 3; i++ {
		_, err = fc.GetSpan(context.Background(), 1)
		require.NoError(t, err)
	}

	assert.Equal(t, primaryBefore, primary.hits.Load(), "primary should not be contacted while sticky")
	assert.Equal(t, secondaryBefore+3, secondary.hits.Load(), "all calls should go to secondary")
}

func TestFailover_ProbeBackToPrimary(t *testing.T) {
	primaryDown := atomic.Bool{}
	primaryDown.Store(true)

	primary := &mockHeimdallClient{
		getSpanFn: func(_ context.Context, spanID uint64) (*types.Span, error) {
			if primaryDown.Load() {
				return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
			}
			return &types.Span{Id: spanID}, nil
		},
	}
	secondary := &mockHeimdallClient{}

	fc := NewFailoverHeimdallClient(primary, secondary)
	fc.attemptTimeout = 100 * time.Millisecond
	fc.cooldown = 50 * time.Millisecond
	defer fc.Close()

	// Trigger failover
	_, err := fc.GetSpan(context.Background(), 1)
	require.NoError(t, err)

	// Wait for cooldown to elapse
	time.Sleep(100 * time.Millisecond)

	// Bring primary back
	primaryDown.Store(false)

	primaryBefore := primary.hits.Load()

	// Next call should probe primary and succeed
	_, err = fc.GetSpan(context.Background(), 1)
	require.NoError(t, err)
	assert.Greater(t, primary.hits.Load(), primaryBefore, "primary should have been probed")

	// Verify we're back on primary
	secondaryBefore := secondary.hits.Load()
	_, err = fc.GetSpan(context.Background(), 1)
	require.NoError(t, err)
	assert.Equal(t, secondaryBefore, secondary.hits.Load(), "should be back on primary now")
}

func TestFailover_ProbeBackFails(t *testing.T) {
	primary := &mockHeimdallClient{
		getSpanFn: func(_ context.Context, _ uint64) (*types.Span, error) {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
		},
	}
	secondary := &mockHeimdallClient{}

	fc := NewFailoverHeimdallClient(primary, secondary)
	fc.attemptTimeout = 100 * time.Millisecond
	fc.cooldown = 50 * time.Millisecond
	defer fc.Close()

	// Trigger failover
	_, err := fc.GetSpan(context.Background(), 1)
	require.NoError(t, err)

	// Wait for cooldown
	time.Sleep(100 * time.Millisecond)

	// Probe should fail, then fallback to secondary
	secondaryBefore := secondary.hits.Load()
	_, err = fc.GetSpan(context.Background(), 1)
	require.NoError(t, err)
	assert.Greater(t, secondary.hits.Load(), secondaryBefore, "should fall back to secondary after failed probe")
}

func TestFailover_ClosesBothClients(t *testing.T) {
	var primaryClosed, secondaryClosed atomic.Bool

	primary := &mockHeimdallClient{closeFn: func() { primaryClosed.Store(true) }}
	secondary := &mockHeimdallClient{closeFn: func() { secondaryClosed.Store(true) }}

	fc := NewFailoverHeimdallClient(primary, secondary)
	fc.Close()

	assert.True(t, primaryClosed.Load(), "primary should be closed")
	assert.True(t, secondaryClosed.Load(), "secondary should be closed")
}

func TestFailover_PassthroughWhenPrimaryHealthy(t *testing.T) {
	primary := &mockHeimdallClient{}
	secondary := &mockHeimdallClient{}

	fc := NewFailoverHeimdallClient(primary, secondary)
	fc.attemptTimeout = 5 * time.Second
	defer fc.Close()

	for i := 0; i < 5; i++ {
		_, err := fc.GetSpan(context.Background(), 1)
		require.NoError(t, err)
	}

	assert.Equal(t, int32(5), primary.hits.Load(), "all calls should go to primary")
	assert.Equal(t, int32(0), secondary.hits.Load(), "secondary should not be contacted")
}

// Integration test using real HTTP servers to verify end-to-end behavior
func TestFailover_Integration_ServiceUnavailable(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(primary.Close)

	secondary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(secondary.Close)

	primaryClient := NewHeimdallClient(primary.URL, 5*time.Second)
	secondaryClient := NewHeimdallClient(secondary.URL, 5*time.Second)

	fc := NewFailoverHeimdallClient(primaryClient, secondaryClient)
	fc.attemptTimeout = 2 * time.Second
	defer fc.Close()

	ctx := WithRequestType(context.Background(), SpanRequest)

	// 503 should NOT trigger failover
	_, err := fc.GetSpan(ctx, 1)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrServiceUnavailable))
}

func TestIsFailoverError(t *testing.T) {
	ctx := context.Background()

	// Transport errors should trigger failover
	netErr := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	assert.True(t, isFailoverError(netErr, ctx), "net.Error should trigger failover")

	// ErrNoResponse should trigger failover
	assert.True(t, isFailoverError(ErrNoResponse, ctx), "ErrNoResponse should trigger failover")

	// ErrNotSuccessfulResponse should trigger failover
	assert.True(t, isFailoverError(fmt.Errorf("wrapped: %w", ErrNotSuccessfulResponse), ctx), "ErrNotSuccessfulResponse should trigger failover")

	// DeadlineExceeded with live caller ctx should trigger failover
	assert.True(t, isFailoverError(context.DeadlineExceeded, ctx), "DeadlineExceeded should trigger failover when caller ctx is alive")

	// ErrShutdownDetected should NOT trigger failover
	assert.False(t, isFailoverError(ErrShutdownDetected, ctx), "ErrShutdownDetected should not trigger failover")

	// ErrServiceUnavailable should NOT trigger failover
	assert.False(t, isFailoverError(ErrServiceUnavailable, ctx), "ErrServiceUnavailable should not trigger failover")

	// Caller context cancelled should NOT trigger failover
	cancelledCtx, cancel := context.WithCancel(ctx)
	cancel()
	assert.False(t, isFailoverError(context.DeadlineExceeded, cancelledCtx), "should not failover when caller ctx is done")

	// nil error should not trigger failover
	assert.False(t, isFailoverError(nil, ctx), "nil error should not trigger failover")
}
