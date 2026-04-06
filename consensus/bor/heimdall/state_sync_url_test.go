package heimdall

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

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
