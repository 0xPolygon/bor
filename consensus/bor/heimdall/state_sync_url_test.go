package heimdall

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStateSyncsAtHeightURL_Format(t *testing.T) {
	t.Parallel()

	fromID := uint64(42)
	heimdallHeight := int64(9999)
	toTime := int64(1700000000) // 2023-11-14T22:13:20Z

	u, err := visibleAtHeightURL("http://bor0", fromID, heimdallHeight, toTime)
	require.NoError(t, err)

	// Path must be clerk/state-syncs-at-height
	require.True(t, strings.HasSuffix(u.Path, "clerk/state-syncs-at-height"),
		"expected path to end with clerk/state-syncs-at-height, got %s", u.Path)

	// Validate individual query parameters using parsed values (not raw string)
	q := u.Query()

	expectedTime := time.Unix(toTime, 0).UTC().Format(time.RFC3339Nano)
	require.Equal(t, expectedTime, q.Get("to_time"), "to_time should be RFC3339Nano formatted")
	require.NotEqual(t, fmt.Sprintf("%d", toTime), q.Get("to_time"), "to_time should NOT be raw unix seconds")
	require.Equal(t, fmt.Sprintf("%d", fromID), q.Get("from_id"))
	require.Equal(t, fmt.Sprintf("%d", heimdallHeight), q.Get("heimdall_height"))
	require.Equal(t, fmt.Sprintf("%d", stateFetchLimit), q.Get("pagination.limit"))
}

func TestBlockHeightByTimeURL_Format(t *testing.T) {
	t.Parallel()

	cutoffTime := int64(1700000000)

	u, err := blockHeightByTimeURL("http://bor0", cutoffTime)
	require.NoError(t, err)

	// Path must be clerk/block-height-by-time
	require.True(t, strings.HasSuffix(u.Path, "clerk/block-height-by-time"),
		"expected path to end with clerk/block-height-by-time, got %s", u.Path)

	// cutoff_time should be raw unix seconds (integer)
	require.Contains(t, u.RawQuery, fmt.Sprintf("cutoff_time=%d", cutoffTime))

	// Full URL sanity check
	expected := fmt.Sprintf("http://bor0/clerk/block-height-by-time?cutoff_time=%d", cutoffTime)
	require.Equal(t, expected, u.String())
}

func TestStateSyncsByTimeURL_Format(t *testing.T) {
	t.Parallel()

	fromID := uint64(42)
	toTime := int64(1700000000) // 2023-11-14T22:13:20Z

	u, err := stateSyncsByTimeURL("http://bor0", fromID, toTime)
	require.NoError(t, err)

	// Path must be clerk/state-syncs-by-time
	require.True(t, strings.HasSuffix(u.Path, "clerk/state-syncs-by-time"),
		"expected path to end with clerk/state-syncs-by-time, got %s", u.Path)

	// Validate query parameters
	q := u.Query()

	expectedTime := time.Unix(toTime, 0).UTC().Format(time.RFC3339Nano)
	require.Equal(t, expectedTime, q.Get("to_time"), "to_time should be RFC3339Nano formatted")
	require.Equal(t, fmt.Sprintf("%d", fromID), q.Get("from_id"))
	require.Equal(t, fmt.Sprintf("%d", stateFetchLimit), q.Get("pagination.limit"))

	// Should NOT have heimdall_height (resolved internally by heimdall)
	require.Empty(t, q.Get("heimdall_height"), "combined endpoint should not send heimdall_height")
}

func TestStateSyncURL_ToTimeIsRFC3339(t *testing.T) {
	t.Parallel()

	fromID := uint64(10)
	to := int64(100)

	u, err := stateSyncURL("http://bor0", fromID, to)
	require.NoError(t, err)

	expectedTime := time.Unix(to, 0).UTC().Format(time.RFC3339Nano)
	require.Contains(t, u.RawQuery, fmt.Sprintf("to_time=%s", expectedTime),
		"to_time should be RFC3339Nano formatted")
}
