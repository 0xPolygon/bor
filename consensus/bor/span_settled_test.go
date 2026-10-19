package bor

import (
	"errors"
	"math/big"
	"testing"
	"time"

	borTypes "github.com/0xPolygon/heimdall-v2/x/bor/types"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

// A rotation starts span 2 inside span 1, so block 400 belongs to both and the
// newer span decides who may seal it.
func rotatedSpans() map[uint64]*borTypes.Span {
	return map[uint64]*borTypes.Span{
		0: makeTestSpan(0, 0, 255, "0x0000000000000000000000000000000000000001"),
		1: makeTestSpan(1, 256, 6655, "0x0000000000000000000000000000000000000001"),
		2: makeTestSpan(2, 300, 6699, "0x0000000000000000000000000000000000000002"),
	}
}

// newSettledTestStore returns a store without the poll loop, whose latest span
// was fetched age ago, or never when age is zero.
func newSettledTestStore(t *testing.T, client *dynamicHeimdallClient, age time.Duration) *SpanStore {
	t.Helper()

	s := NewSpanStore(nil, nil, "1337")
	s.heimdallClient = client
	require.NoError(t, s.updateHeimdallStatus(t.Context()))

	if age > 0 {
		require.NoError(t, s.updateLatestSpan(t.Context()))
		s.latestSpanAt.Store(time.Now().Add(-age).UnixNano())
	}

	return s
}

func TestSettledSpanByBlockNumber(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		spans       func() map[uint64]*borTypes.Span
		latest      uint64
		age         time.Duration
		failLatest  bool
		wantSpan    uint64
		wantSettled bool
	}{
		{name: "fresh latest span", latest: 2, age: time.Millisecond, wantSpan: 2, wantSettled: true},
		{name: "fresh latest span, no newer span", latest: 1, age: time.Millisecond, wantSpan: 1, wantSettled: true},
		{name: "latest span stale", latest: 1, age: time.Minute, wantSpan: 1},
		{name: "latest span just inside freshness", latest: 1, age: latestSpanFreshness - time.Second, wantSpan: 1, wantSettled: true},
		{name: "latest span just past freshness", latest: 1, age: latestSpanFreshness + time.Second, wantSpan: 1},
		{name: "latest span fetch fails", latest: 2, failLatest: true, wantSpan: 1},
		{
			name: "newer span lookup fails",
			spans: func() map[uint64]*borTypes.Span {
				spans := rotatedSpans()
				spans[4] = makeTestSpan(4, 7000, 13399, "0x0000000000000000000000000000000000000004")
				return spans
			},
			latest:   4,
			age:      time.Millisecond,
			wantSpan: 2,
		},
		{
			name: "two later spans start past the block",
			spans: func() map[uint64]*borTypes.Span {
				spans := rotatedSpans()
				spans[3] = makeTestSpan(3, 7000, 13399, "0x0000000000000000000000000000000000000003")
				spans[4] = makeTestSpan(4, 7100, 13499, "0x0000000000000000000000000000000000000004")
				return spans
			},
			latest:      4,
			age:         time.Minute,
			wantSpan:    2,
			wantSettled: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			spans := rotatedSpans()
			if tc.spans != nil {
				spans = tc.spans()
			}

			client := newDynamicHeimdallClient()
			client.setSpans(spans, spans[tc.latest])
			s := newSettledTestStore(t, client, tc.age)

			// The spans the node already knows; only the latest-span fetch fails.
			for id := uint64(0); id <= 1; id++ {
				_, err := s.spanById(t.Context(), id)
				require.NoError(t, err)
			}
			if tc.failLatest {
				client.setError(errors.New("heimdall unreachable"))
			}

			span, settled, err := s.settledSpanByBlockNumber(t.Context(), 400)
			require.NoError(t, err)
			require.Equal(t, tc.wantSpan, span.Id)
			require.Equal(t, tc.wantSettled, settled)

			plain, err := s.spanByBlockNumber(t.Context(), 400)
			require.NoError(t, err)
			require.Equal(t, tc.wantSpan, plain.Id)
		})
	}
}

func TestVeBlopSnapshotCarriesSpanSettledness(t *testing.T) {
	t.Parallel()

	newSigner := common.HexToAddress("0x0000000000000000000000000000000000000002")

	cases := []struct {
		name      string
		age       time.Duration
		unsettled bool
	}{
		{name: "fresh latest span", age: time.Millisecond},
		{name: "stale latest span", age: time.Minute, unsettled: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			spans := rotatedSpans()
			delete(spans, 2) // the latest span this node sees is 1, fetched age ago
			client := newDynamicHeimdallClient()
			client.setSpans(spans, spans[1])

			borCfg := &params.BorConfig{Sprint: map[string]uint64{"0": 64}, Period: map[string]uint64{"0": 2}, RioBlock: big.NewInt(0)}
			chain, b := newChainAndBorForTest(t, &fakeSpanner{}, borCfg, false, common.Address{}, uint64(time.Now().Unix()))
			b.spanStore = newSettledTestStore(t, client, tc.age)

			snap, err := b.getVeBlopSnapshot(chain.HeaderChain(), &types.Header{Number: big.NewInt(400)}, nil, false)
			require.NoError(t, err)
			require.Equal(t, tc.unsettled, snap.spanUnsettled)
			require.Equal(t, tc.unsettled, snap.copy().spanUnsettled)

			_, err = snap.GetSignerSuccessionNumber(newSigner)
			var signerErr *UnauthorizedSignerError
			require.ErrorAs(t, err, &signerErr)
			require.Equal(t, !tc.unsettled, errors.Is(err, consensus.ErrUnauthorizedSigner))
		})
	}
}
