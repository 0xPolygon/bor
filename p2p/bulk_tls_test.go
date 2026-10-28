package p2p

import (
	"crypto/tls"
	"crypto/x509"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBulkTLSCertificateValidation(t *testing.T) {
	cert, err := generateBulkSidecarCertificate()
	require.NoError(t, err)
	require.ErrorContains(t, verifyBulkSidecarTLSConnection(tls.ConnectionState{}), "missing")
	for _, test := range []struct {
		name   string
		change func(*x509.Certificate)
		want   string
	}{
		{"valid", func(*x509.Certificate) {}, ""},
		{"expired", func(c *x509.Certificate) { c.NotAfter = time.Now().Add(-time.Hour) }, "expired"},
		{"future", func(c *x509.Certificate) { c.NotBefore = time.Now().Add(time.Hour) }, "not yet valid"},
		{"name", func(c *x509.Certificate) { c.DNSNames = []string{"other.example"} }, "name invalid"},
		{"key usage", func(c *x509.Certificate) { c.KeyUsage = x509.KeyUsageKeyEncipherment }, "digital signature"},
		{"extended usage", func(c *x509.Certificate) { c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth} }, "server auth"},
		{"unspecified extended usage", func(c *x509.Certificate) { c.ExtKeyUsage = nil }, ""},
		{"signature", func(c *x509.Certificate) { c.Signature = []byte{1, 2, 3} }, "signature invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			leaf := *cert.Leaf
			test.change(&leaf)
			err := verifyBulkSidecarTLSConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{&leaf}})
			if test.want == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, test.want)
			}
		})
	}
}
