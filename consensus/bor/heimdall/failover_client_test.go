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

// mockHeimdallClient is a configurable mock implementing the Endpoint interface.
type mockHeimdallClient struct {
	getSpanFn            func(ctx context.Context, spanID uint64) (*types.Span, error)
	getLatestSpanFn      func(ctx context.Context) (*types.Span, error)
	stateSyncEventsFn    func(ctx context.Context, fromID uint64, to int64) ([]*clerk.EventRecordWithTime, error)
	fetchCheckpointFn    func(ctx context.Context, number int64) (*checkpoint.Checkpoint, error)
	fetchCheckpointCntFn func(ctx context.Context) (int64, error)
	fetchMilestoneFn     func(ctx context.Context) (*milestone.Milestone, error)
	fetchMilestoneCntFn  func(ctx context.Context) (int64, error)
	fetchStatusFn        func(ctx context.Context) (*ctypes.SyncInfo, error)
	closeFn              func()
	hits                 atomic.Int32
}

func (m *mockHeimdallClient) StateSyncEvents(ctx context.Context, fromID uint64, to int64) ([]*clerk.EventRecordWithTime, error) {
	m.hits.Add(1)

	if m.stateSyncEventsFn != nil {
		return m.stateSyncEventsFn(ctx, fromID, to)
	}

	return []*clerk.EventRecordWithTime{}, nil
}

func (m *mockHeimdallClient) GetSpan(ctx context.Context, spanID uint64) (*types.Span, error) {
	m.hits.Add(1)

	if m.getSpanFn != nil {
		return m.getSpanFn(ctx, spanID)
	}

	return &types.Span{Id: spanID}, nil
}

func (m *mockHeimdallClient) GetLatestSpan(ctx context.Context) (*types.Span, error) {
	m.hits.Add(1)

	if m.getLatestSpanFn != nil {
		return m.getLatestSpanFn(ctx)
	}

	return &types.Span{Id: 99}, nil
}

func (m *mockHeimdallClient) FetchCheckpoint(ctx context.Context, number int64) (*checkpoint.Checkpoint, error) {
	m.hits.Add(1)

	if m.fetchCheckpointFn != nil {
		return m.fetchCheckpointFn(ctx, number)
	}

	return &checkpoint.Checkpoint{}, nil
}

func (m *mockHeimdallClient) FetchCheckpointCount(ctx context.Context) (int64, error) {
	m.hits.Add(1)

	if m.fetchCheckpointCntFn != nil {
		return m.fetchCheckpointCntFn(ctx)
	}

	return 10, nil
}

func (m *mockHeimdallClient) FetchMilestone(ctx context.Context) (*milestone.Milestone, error) {
	m.hits.Add(1)

	if m.fetchMilestoneFn != nil {
		return m.fetchMilestoneFn(ctx)
	}

	return &milestone.Milestone{}, nil
}

func (m *mockHeimdallClient) FetchMilestoneCount(ctx context.Context) (int64, error) {
	m.hits.Add(1)

	if m.fetchMilestoneCntFn != nil {
		return m.fetchMilestoneCntFn(ctx)
	}

	return 5, nil
}

func (m *mockHeimdallClient) FetchStatus(ctx context.Context) (*ctypes.SyncInfo, error) {
	m.hits.Add(1)

	if m.fetchStatusFn != nil {
		return m.fetchStatusFn(ctx)
	}

	return &ctypes.SyncInfo{}, nil
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

	fc := NewMultiHeimdallClient(primary, secondary)
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

	fc := NewMultiHeimdallClient(primary, secondary)
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

	fc := NewMultiHeimdallClient(primary, secondary)
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

	fc := NewMultiHeimdallClient(primary, secondary)
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

	fc := NewMultiHeimdallClient(primary, secondary)
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

	fc := NewMultiHeimdallClient(primary, secondary)
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

	fc := NewMultiHeimdallClient(primary, secondary)
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

	fc := NewMultiHeimdallClient(primary, secondary)
	fc.Close()

	assert.True(t, primaryClosed.Load(), "primary should be closed")
	assert.True(t, secondaryClosed.Load(), "secondary should be closed")
}

func TestFailover_PassthroughWhenPrimaryHealthy(t *testing.T) {
	primary := &mockHeimdallClient{}
	secondary := &mockHeimdallClient{}

	fc := NewMultiHeimdallClient(primary, secondary)
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

	fc := NewMultiHeimdallClient(primaryClient, secondaryClient)
	fc.attemptTimeout = 2 * time.Second
	defer fc.Close()

	ctx := WithRequestType(context.Background(), SpanRequest)

	// 503 should NOT trigger failover
	_, err := fc.GetSpan(ctx, 1)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrServiceUnavailable))
}

func TestFailover_StateSyncEvents(t *testing.T) {
	primary := &mockHeimdallClient{
		stateSyncEventsFn: func(_ context.Context, _ uint64, _ int64) ([]*clerk.EventRecordWithTime, error) {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
		},
	}
	secondary := &mockHeimdallClient{
		stateSyncEventsFn: func(_ context.Context, fromID uint64, to int64) ([]*clerk.EventRecordWithTime, error) {
			return []*clerk.EventRecordWithTime{{EventRecord: clerk.EventRecord{ID: fromID}}}, nil
		},
	}

	fc := NewMultiHeimdallClient(primary, secondary)
	fc.attemptTimeout = 100 * time.Millisecond
	defer fc.Close()

	events, err := fc.StateSyncEvents(context.Background(), 42, 100)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, uint64(42), events[0].ID)
	assert.Equal(t, int32(1), secondary.hits.Load())
}

func TestFailover_GetLatestSpan(t *testing.T) {
	primary := &mockHeimdallClient{
		getLatestSpanFn: func(_ context.Context) (*types.Span, error) {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
		},
	}
	secondary := &mockHeimdallClient{
		getLatestSpanFn: func(_ context.Context) (*types.Span, error) {
			return &types.Span{Id: 77}, nil
		},
	}

	fc := NewMultiHeimdallClient(primary, secondary)
	fc.attemptTimeout = 100 * time.Millisecond
	defer fc.Close()

	span, err := fc.GetLatestSpan(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint64(77), span.Id)
	assert.Equal(t, int32(1), secondary.hits.Load())
}

func TestFailover_FetchCheckpoint(t *testing.T) {
	primary := &mockHeimdallClient{
		fetchCheckpointFn: func(_ context.Context, _ int64) (*checkpoint.Checkpoint, error) {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
		},
	}
	secondary := &mockHeimdallClient{}

	fc := NewMultiHeimdallClient(primary, secondary)
	fc.attemptTimeout = 100 * time.Millisecond
	defer fc.Close()

	cp, err := fc.FetchCheckpoint(context.Background(), 5)
	require.NoError(t, err)
	require.NotNil(t, cp)
	assert.Equal(t, int32(1), secondary.hits.Load())
}

func TestFailover_FetchCheckpointCount(t *testing.T) {
	primary := &mockHeimdallClient{
		fetchCheckpointCntFn: func(_ context.Context) (int64, error) {
			return 0, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
		},
	}
	secondary := &mockHeimdallClient{}

	fc := NewMultiHeimdallClient(primary, secondary)
	fc.attemptTimeout = 100 * time.Millisecond
	defer fc.Close()

	count, err := fc.FetchCheckpointCount(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(10), count)
	assert.Equal(t, int32(1), secondary.hits.Load())
}

func TestFailover_FetchMilestone(t *testing.T) {
	primary := &mockHeimdallClient{
		fetchMilestoneFn: func(_ context.Context) (*milestone.Milestone, error) {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
		},
	}
	secondary := &mockHeimdallClient{}

	fc := NewMultiHeimdallClient(primary, secondary)
	fc.attemptTimeout = 100 * time.Millisecond
	defer fc.Close()

	ms, err := fc.FetchMilestone(context.Background())
	require.NoError(t, err)
	require.NotNil(t, ms)
	assert.Equal(t, int32(1), secondary.hits.Load())
}

func TestFailover_FetchMilestoneCount(t *testing.T) {
	primary := &mockHeimdallClient{
		fetchMilestoneCntFn: func(_ context.Context) (int64, error) {
			return 0, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
		},
	}
	secondary := &mockHeimdallClient{}

	fc := NewMultiHeimdallClient(primary, secondary)
	fc.attemptTimeout = 100 * time.Millisecond
	defer fc.Close()

	count, err := fc.FetchMilestoneCount(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(5), count)
	assert.Equal(t, int32(1), secondary.hits.Load())
}

func TestFailover_FetchStatus(t *testing.T) {
	primary := &mockHeimdallClient{
		fetchStatusFn: func(_ context.Context) (*ctypes.SyncInfo, error) {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
		},
	}
	secondary := &mockHeimdallClient{}

	fc := NewMultiHeimdallClient(primary, secondary)
	fc.attemptTimeout = 100 * time.Millisecond
	defer fc.Close()

	status, err := fc.FetchStatus(context.Background())
	require.NoError(t, err)
	require.NotNil(t, status)
	assert.Equal(t, int32(1), secondary.hits.Load())
}

func TestFailover_ProbeBackNonFailoverError(t *testing.T) {
	primary := &mockHeimdallClient{
		getSpanFn: func(_ context.Context, _ uint64) (*types.Span, error) {
			return nil, ErrShutdownDetected
		},
	}
	secondary := &mockHeimdallClient{}

	fc := NewMultiHeimdallClient(primary, secondary)
	fc.attemptTimeout = 100 * time.Millisecond
	fc.cooldown = 50 * time.Millisecond
	defer fc.Close()

	// Force onto secondary
	fc.mu.Lock()
	fc.active = 1
	fc.lastSwitch = time.Now().Add(-time.Hour) // cooldown already elapsed
	fc.mu.Unlock()

	// Probe primary → gets ErrShutdownDetected (non-failover error)
	// Should return the error directly, NOT fall back to secondary
	secondaryBefore := secondary.hits.Load()
	_, err := fc.GetSpan(context.Background(), 1)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrShutdownDetected))
	assert.Equal(t, secondaryBefore, secondary.hits.Load(), "should not fall back to secondary on non-failover error during probe")
}

func TestFailover_SwitchOnPrimarySubContextError(t *testing.T) {
	tests := []struct {
		name      string
		primaryFn func(ctx context.Context, _ uint64) (*types.Span, error)
	}{
		{
			name: "DeadlineExceeded",
			primaryFn: func(ctx context.Context, _ uint64) (*types.Span, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
		},
		{
			name: "Canceled",
			primaryFn: func(_ context.Context, _ uint64) (*types.Span, error) {
				return nil, context.Canceled
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			primary := &mockHeimdallClient{getSpanFn: tt.primaryFn}
			secondary := &mockHeimdallClient{}

			fc := NewMultiHeimdallClient(primary, secondary)
			fc.attemptTimeout = 100 * time.Millisecond
			defer fc.Close()

			span, err := fc.GetSpan(context.Background(), 1)
			require.NoError(t, err)
			require.NotNil(t, span)
			assert.Equal(t, int32(1), primary.hits.Load(), "primary should have been tried")
			assert.Equal(t, int32(1), secondary.hits.Load(), "should failover on sub-context error")
		})
	}
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

	// Canceled with live caller ctx should trigger failover (sub-context was canceled, not the caller)
	assert.True(t, isFailoverError(context.Canceled, ctx), "Canceled should trigger failover when caller ctx is alive")

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

func TestFailover_ThreeClients_CascadeToTertiary(t *testing.T) {
	primary := &mockHeimdallClient{
		getSpanFn: func(_ context.Context, _ uint64) (*types.Span, error) {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
		},
	}
	secondary := &mockHeimdallClient{
		getSpanFn: func(_ context.Context, _ uint64) (*types.Span, error) {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
		},
	}
	tertiary := &mockHeimdallClient{}

	fc := NewMultiHeimdallClient(primary, secondary, tertiary)
	fc.attemptTimeout = 100 * time.Millisecond
	defer fc.Close()

	span, err := fc.GetSpan(context.Background(), 1)
	require.NoError(t, err)
	require.NotNil(t, span)

	assert.GreaterOrEqual(t, primary.hits.Load(), int32(1), "primary should have been tried")
	assert.GreaterOrEqual(t, secondary.hits.Load(), int32(1), "secondary should have been tried")
	assert.Equal(t, int32(1), tertiary.hits.Load(), "tertiary should have been called once")
}

func TestFailover_AllClientsFail(t *testing.T) {
	connErr := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}

	primary := &mockHeimdallClient{
		getSpanFn: func(_ context.Context, _ uint64) (*types.Span, error) { return nil, connErr },
	}
	secondary := &mockHeimdallClient{
		getSpanFn: func(_ context.Context, _ uint64) (*types.Span, error) { return nil, connErr },
	}
	tertiary := &mockHeimdallClient{
		getSpanFn: func(_ context.Context, _ uint64) (*types.Span, error) { return nil, connErr },
	}

	fc := NewMultiHeimdallClient(primary, secondary, tertiary)
	fc.attemptTimeout = 100 * time.Millisecond
	defer fc.Close()

	_, err := fc.GetSpan(context.Background(), 1)
	require.Error(t, err)
}

func TestFailover_ThreeClients_ProbeBackToPrimary(t *testing.T) {
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
	secondary := &mockHeimdallClient{
		getSpanFn: func(_ context.Context, _ uint64) (*types.Span, error) {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
		},
	}
	tertiary := &mockHeimdallClient{}

	fc := NewMultiHeimdallClient(primary, secondary, tertiary)
	fc.attemptTimeout = 100 * time.Millisecond
	fc.cooldown = 50 * time.Millisecond
	defer fc.Close()

	// Trigger cascade to tertiary
	_, err := fc.GetSpan(context.Background(), 1)
	require.NoError(t, err)

	// Wait for cooldown
	time.Sleep(100 * time.Millisecond)

	// Bring primary back
	primaryDown.Store(false)
	primaryBefore := primary.hits.Load()

	// Next call should probe primary and succeed
	_, err = fc.GetSpan(context.Background(), 1)
	require.NoError(t, err)
	assert.Greater(t, primary.hits.Load(), primaryBefore, "primary should have been probed")

	// Verify we're back on primary
	tertiaryBefore := tertiary.hits.Load()
	_, err = fc.GetSpan(context.Background(), 1)
	require.NoError(t, err)
	assert.Equal(t, tertiaryBefore, tertiary.hits.Load(), "should be back on primary now")
}

// Tests for the shouldProbe path (lines 156-161): probe primary fails with
// failover error, then current (non-primary) client also fails.
func TestFailover_ProbeCurrentNonFailoverError(t *testing.T) {
	// Probe primary → failover error, current (secondary) → non-failover error.
	// Should return the non-failover error without cascading to tertiary.
	primary := &mockHeimdallClient{
		getSpanFn: func(_ context.Context, _ uint64) (*types.Span, error) {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
		},
	}
	secondary := &mockHeimdallClient{
		getSpanFn: func(_ context.Context, _ uint64) (*types.Span, error) {
			return nil, ErrShutdownDetected
		},
	}
	tertiary := &mockHeimdallClient{}

	fc := NewMultiHeimdallClient(primary, secondary, tertiary)
	fc.attemptTimeout = 100 * time.Millisecond
	fc.cooldown = 50 * time.Millisecond
	defer fc.Close()

	// Force onto secondary with cooldown elapsed so probe triggers.
	fc.mu.Lock()
	fc.active = 1
	fc.lastSwitch = time.Now().Add(-time.Hour)
	fc.mu.Unlock()

	_, err := fc.GetSpan(context.Background(), 1)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrShutdownDetected))
	assert.Equal(t, int32(0), tertiary.hits.Load(), "should not cascade to tertiary on non-failover error")
}

func TestFailover_ProbeCurrentFailoverError_CascadesToNext(t *testing.T) {
	// Probe primary → failover error, current (secondary) → failover error.
	// Should cascade to tertiary.
	connErr := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}

	primary := &mockHeimdallClient{
		getSpanFn: func(_ context.Context, _ uint64) (*types.Span, error) { return nil, connErr },
	}
	secondary := &mockHeimdallClient{
		getSpanFn: func(_ context.Context, _ uint64) (*types.Span, error) { return nil, connErr },
	}
	tertiary := &mockHeimdallClient{}

	fc := NewMultiHeimdallClient(primary, secondary, tertiary)
	fc.attemptTimeout = 100 * time.Millisecond
	fc.cooldown = 50 * time.Millisecond
	defer fc.Close()

	// Force onto secondary with cooldown elapsed so probe triggers.
	fc.mu.Lock()
	fc.active = 1
	fc.lastSwitch = time.Now().Add(-time.Hour)
	fc.mu.Unlock()

	span, err := fc.GetSpan(context.Background(), 1)
	require.NoError(t, err)
	require.NotNil(t, span)
	assert.Equal(t, int32(1), tertiary.hits.Load(), "should cascade to tertiary")

	fc.mu.Lock()
	assert.Equal(t, 2, fc.active, "active should switch to tertiary")
	fc.mu.Unlock()
}

// Tests for the active != 0 no-probe path (lines 171-176): on a non-primary
// client with cooldown not elapsed, the current client fails.
func TestFailover_StickyNonFailoverError(t *testing.T) {
	// Sticky on secondary (cooldown not elapsed), secondary returns non-failover error.
	// Should return error without cascading to tertiary.
	primary := &mockHeimdallClient{}
	secondary := &mockHeimdallClient{
		getSpanFn: func(_ context.Context, _ uint64) (*types.Span, error) {
			return nil, ErrShutdownDetected
		},
	}
	tertiary := &mockHeimdallClient{}

	fc := NewMultiHeimdallClient(primary, secondary, tertiary)
	fc.attemptTimeout = 100 * time.Millisecond
	fc.cooldown = 1 * time.Hour // very long — no probe
	defer fc.Close()

	// Force onto secondary with recent switch (cooldown not elapsed).
	fc.mu.Lock()
	fc.active = 1
	fc.lastSwitch = time.Now()
	fc.mu.Unlock()

	_, err := fc.GetSpan(context.Background(), 1)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrShutdownDetected))
	assert.Equal(t, int32(0), primary.hits.Load(), "should not probe primary")
	assert.Equal(t, int32(0), tertiary.hits.Load(), "should not cascade to tertiary on non-failover error")
}

func TestFailover_StickyFailoverError_CascadesToNext(t *testing.T) {
	// Sticky on secondary (cooldown not elapsed), secondary returns failover error.
	// Should cascade to tertiary.
	connErr := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}

	primary := &mockHeimdallClient{}
	secondary := &mockHeimdallClient{
		getSpanFn: func(_ context.Context, _ uint64) (*types.Span, error) { return nil, connErr },
	}
	tertiary := &mockHeimdallClient{}

	fc := NewMultiHeimdallClient(primary, secondary, tertiary)
	fc.attemptTimeout = 100 * time.Millisecond
	fc.cooldown = 1 * time.Hour // very long — no probe
	defer fc.Close()

	// Force onto secondary with recent switch (cooldown not elapsed).
	fc.mu.Lock()
	fc.active = 1
	fc.lastSwitch = time.Now()
	fc.mu.Unlock()

	span, err := fc.GetSpan(context.Background(), 1)
	require.NoError(t, err)
	require.NotNil(t, span)
	assert.Equal(t, int32(0), primary.hits.Load(), "should not probe primary")
	assert.Equal(t, int32(1), tertiary.hits.Load(), "should cascade to tertiary")

	fc.mu.Lock()
	assert.Equal(t, 2, fc.active, "active should switch to tertiary")
	fc.mu.Unlock()
}

func TestFailover_ClosesAllClients(t *testing.T) {
	var closed [3]atomic.Bool

	clients := make([]Endpoint, 3)
	for i := range clients {
		idx := i
		clients[i] = &mockHeimdallClient{closeFn: func() { closed[idx].Store(true) }}
	}

	fc := NewMultiHeimdallClient(clients...)
	fc.Close()

	for i := range closed {
		assert.True(t, closed[i].Load(), "client %d should be closed", i)
	}
}
