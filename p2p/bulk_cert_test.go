package p2p

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBulkCertRotatorReusesValidCertificate(t *testing.T) {
	certs := new(bulkCertRotator)
	first, err := certs.certificate(nil)
	require.NoError(t, err)
	require.NotNil(t, first.Leaf, "leaf must be parsed so expiry is readable without re-parsing")

	second, err := certs.certificate(nil)
	require.NoError(t, err)
	require.Same(t, first, second, "a certificate inside its window must not be regenerated")
}

// A node that stays up past bulkSidecarCertLifetime must keep presenting a
// valid certificate; peers reject an expired one outright and fall back to
// RLPx with no recovery short of a restart.
func TestBulkCertRotatorRenewsBeforeExpiry(t *testing.T) {
	certs := new(bulkCertRotator)
	first, err := certs.certificate(nil)
	require.NoError(t, err)

	// Step to just inside the refresh window ahead of expiry.
	renewAt := first.Leaf.NotAfter.Add(-bulkSidecarCertRefresh).Add(time.Second)
	rotated, err := certs.currentAt(renewAt)
	require.NoError(t, err)
	require.NotSame(t, first, rotated, "certificate must be renewed before it expires")
	require.True(t, rotated.Leaf.NotAfter.After(first.Leaf.NotAfter),
		"the replacement window must extend past the one it replaces")
	require.True(t, rotated.Leaf.NotBefore.Before(renewAt), "replacement must be valid when issued")

	// Still valid well before its own refresh point, so rotation is not a loop.
	again, err := certs.currentAt(renewAt.Add(time.Hour))
	require.NoError(t, err)
	require.Same(t, rotated, again)
}

// crypto/tls aborts the handshake whenever GetCertificate returns a non-nil
// error, so a failed rotation must keep serving the previous certificate rather
// than reporting the failure upward.
func TestBulkCertRotatorKeepsPreviousOnFailure(t *testing.T) {
	certs := new(bulkCertRotator)
	first, err := certs.certificate(nil)
	require.NoError(t, err)

	certs.generate = func(time.Time) (tls.Certificate, error) {
		return tls.Certificate{}, errors.New("no entropy")
	}
	renewAt := first.Leaf.NotAfter.Add(-bulkSidecarCertRefresh).Add(time.Second)
	served, err := certs.currentAt(renewAt)
	require.NoError(t, err, "a rotation failure must not abort the handshake")
	require.Same(t, first, served)

	// Recovery on the next handshake once generation works again.
	certs.generate = nil
	rotated, err := certs.currentAt(renewAt)
	require.NoError(t, err)
	require.NotSame(t, first, rotated)
}

// With no certificate yet there is nothing to fall back to, so the failure must
// surface — that is what makes newBulkSidecar fail fast at startup.
func TestBulkCertRotatorReportsFirstFailure(t *testing.T) {
	certs := &bulkCertRotator{generate: func(time.Time) (tls.Certificate, error) {
		return tls.Certificate{}, errors.New("no entropy")
	}}
	cert, err := certs.certificate(nil)
	require.Error(t, err)
	require.Nil(t, cert)
}

func TestBulkCertRotatorConcurrentHandshakes(t *testing.T) {
	certs := new(bulkCertRotator)

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cert, err := certs.certificate(&tls.ClientHelloInfo{})
			require.NoError(t, err)
			require.NotNil(t, cert.Leaf)
		}()
	}
	wg.Wait()
}

// The listener must serve through the rotator rather than pinning one
// certificate for the process lifetime.
func TestBulkSidecarListenerUsesRotator(t *testing.T) {
	server := newTestBulkServer(t)
	t.Cleanup(server.close)

	require.NotNil(t, server.bulk.tls.GetCertificate)
	require.Empty(t, server.bulk.tls.Certificates, "a pinned certificate would bypass rotation")

	cert, err := server.bulk.tls.GetCertificate(&tls.ClientHelloInfo{ServerName: bulkSidecarTLSServerName})
	require.NoError(t, err)
	require.NoError(t, verifyBulkSidecarTLSConnection(tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{cert.Leaf},
	}))
}

func TestBulkCertRotationFailureValidity(t *testing.T) {
	first, err := generateBulkSidecarCertificate()
	require.NoError(t, err)
	for _, test := range []struct {
		name  string
		now   time.Time
		valid bool
	}{
		{"before validity", first.Leaf.NotBefore.Add(-time.Second), false},
		{"refresh boundary", first.Leaf.NotAfter.Add(-bulkSidecarCertRefresh), true},
		{"before expiry", first.Leaf.NotAfter.Add(-time.Nanosecond), true},
		{"expiry", first.Leaf.NotAfter, false},
		{"after expiry", first.Leaf.NotAfter.Add(time.Hour), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			want := errors.New("certificate generation failed")
			certs := &bulkCertRotator{cert: &first, generate: func(time.Time) (tls.Certificate, error) {
				return tls.Certificate{}, want
			}}
			got, err := certs.currentAt(test.now)
			if test.valid {
				require.NoError(t, err)
				require.Same(t, &first, got)
			} else {
				require.ErrorIs(t, err, want)
				require.Nil(t, got)
			}
			certs.generate = nil
			got, err = certs.currentAt(test.now)
			require.NoError(t, err)
			require.NotSame(t, &first, got)
			require.True(t, got.Leaf.NotAfter.After(test.now))
		})
	}
}

func TestBulkCertRotationTLSHandshake(t *testing.T) {
	expired, err := generateBulkSidecarCertificateAt(time.Now().Add(-2 * bulkSidecarCertLifetime))
	require.NoError(t, err)
	certs := &bulkCertRotator{cert: &expired}
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()
	server := tls.Server(serverConn, &tls.Config{
		GetCertificate: certs.certificate,
		NextProtos:     []string{bulkSidecarNextProto},
		MinVersion:     tls.VersionTLS13,
	})
	client := tls.Client(clientConn, newBulkSidecarVerifiedTLSConfig())
	done := make(chan error, 1)
	go func() { done <- server.HandshakeContext(t.Context()) }()
	require.NoError(t, client.HandshakeContext(t.Context()))
	require.NoError(t, <-done)
	require.NotEqual(t, expired.Certificate[0], client.ConnectionState().PeerCertificates[0].Raw)
}
