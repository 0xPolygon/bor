package p2p

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"
)

func TestBulkPendingSourceIsolation(t *testing.T) {
	for _, ip := range []string{"192.0.2.1", "2001:db8::1"} {
		t.Run(ip, func(t *testing.T) {
			admission := newBulkPendingAdmission(3)
			info := &quic.ClientInfo{RemoteAddr: &net.UDPAddr{IP: net.ParseIP(ip), Port: 1}}
			first, err := admission.acquire(info)
			require.NoError(t, err)
			second, err := admission.acquire(info)
			require.NoError(t, err)
			info.RemoteAddr.(*net.UDPAddr).Port++
			_, err = admission.acquire(info)
			require.ErrorIs(t, err, errBulkAdmissionLimit)
			other, err := admission.acquire(bulkTestClientInfo(2))
			require.NoError(t, err, "one source must leave capacity for another")
			_, err = admission.acquire(bulkTestClientInfo(3))
			require.ErrorIs(t, err, errBulkAdmissionLimit)
			first()
			first()
			replacement, err := admission.acquire(info)
			require.NoError(t, err)
			second()
			other()
			replacement()
			require.Zero(t, admission.count)
			require.Empty(t, admission.sources)
		})
	}
}

func TestBulkPendingLocalSourcesKeepGlobalLimit(t *testing.T) {
	admission := newBulkPendingAdmission(4)
	info := &quic.ClientInfo{RemoteAddr: &net.UDPAddr{IP: net.ParseIP("127.0.0.1")}}
	for range 4 {
		release, err := admission.acquire(info)
		require.NoError(t, err)
		t.Cleanup(release)
	}
	_, err := admission.acquire(info)
	require.ErrorIs(t, err, errBulkAdmissionLimit)
}

func TestBulkPendingSourceAddressValidation(t *testing.T) {
	admission := newBulkPendingAdmission(10)
	for _, info := range []*quic.ClientInfo{nil, {}, {RemoteAddr: &net.TCPAddr{}}, {RemoteAddr: (*net.UDPAddr)(nil)}, {RemoteAddr: &net.UDPAddr{}}, {RemoteAddr: &net.UDPAddr{IP: net.IPv4zero}}} {
		_, err := admission.acquire(info)
		require.ErrorIs(t, err, errBulkAdmissionLimit)
	}
	for _, ip := range []net.IP{net.IP{192, 0, 2, 1}, net.ParseIP("::ffff:192.0.2.1")} {
		release, err := admission.acquire(&quic.ClientInfo{RemoteAddr: &net.UDPAddr{IP: ip}})
		require.NoError(t, err)
		t.Cleanup(release)
	}
	_, err := admission.acquire(bulkTestClientInfo(1))
	require.ErrorIs(t, err, errBulkAdmissionLimit, "IPv4 representations share the same allowance")
}

func TestBulkPendingSourceReleasedAfterAuthentication(t *testing.T) {
	admit := newBulkConnContext(10, 4, newBulkQUICWindows(10, 4))
	var pending []context.Context
	for range bulkPendingPerSource {
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		ctx, err := admit(ctx, bulkTestClientInfo(1))
		require.NoError(t, err)
		pending = append(pending, ctx)
	}
	_, err := admit(t.Context(), bulkTestClientInfo(1))
	require.ErrorIs(t, err, errBulkAdmissionLimit)
	releaseBulkPendingAuth(pending[0])
	releaseBulkPendingAuth(pending[0])
	ctx, cancel := context.WithCancel(t.Context())
	_, err = admit(ctx, bulkTestClientInfo(1))
	require.NoError(t, err)
	cancel()
	require.Eventually(t, func() bool {
		ctx, err = admit(t.Context(), bulkTestClientInfo(1))
		return err == nil
	}, time.Second, time.Millisecond)
	releaseBulkPendingAuth(ctx)
}

func TestBulkSidecarRequiresSourceValidation(t *testing.T) {
	server := newTestBulkServer(t)
	t.Cleanup(server.close)
	require.NotNil(t, server.bulk.transport.VerifySourceAddress)
	require.True(t, server.bulk.transport.VerifySourceAddress(bulkTestClientInfo(1).RemoteAddr))
}
