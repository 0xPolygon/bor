// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package p2p

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	crand "crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math/big"
	"slices"
	"sync"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p/enode"
)

func (b *BulkSidecar) acceptAuth(conn *quic.Conn, stream *quic.Stream) (authenticated *Peer, err error) {
	defer func() { err = errors.Join(err, stream.Close()) }()
	binding, err := bulkTLSBinding(conn)
	if err != nil {
		return nil, err
	}
	var hello bulkAuthHello
	if err := readBulkControl(stream, bulkAuthControlMaxSize, &hello); err != nil {
		return nil, err
	}
	if hello.Version != bulkSidecarVersion {
		return nil, fmt.Errorf("unsupported bulk auth version %d", hello.Version)
	}
	if hello.To != b.localID {
		return nil, errors.New("bulk auth remote target mismatch")
	}
	peer := b.srv.Peer(hello.From)
	if peer == nil || peer.Node() == nil {
		return nil, errBulkSidecarNoPeer
	}
	remote := peer.Node()
	remoteKey := remote.Pubkey()
	if remoteKey == nil {
		return nil, errors.New("bulk auth peer missing pubkey")
	}
	var challenge bulkAuthChallenge
	if _, err := io.ReadFull(crand.Reader, challenge.Nonce[:]); err != nil {
		return nil, err
	}
	hash := bulkAuthTranscriptHash(hello.From, hello.To, hello.Nonce, challenge.Nonce, binding, "server")
	sig, err := crypto.Sign(hash, b.priv)
	if err != nil {
		return nil, err
	}
	challenge.Signature = slices.Clone(sig[:64])
	if err := writeBulkControl(stream, challenge); err != nil {
		return nil, err
	}
	var response bulkAuthResponse
	if err := readBulkControl(stream, bulkAuthControlMaxSize, &response); err != nil {
		return nil, err
	}
	if len(response.Signature) != 64 {
		return nil, errors.New("bulk auth response signature length invalid")
	}
	hash = bulkAuthTranscriptHash(hello.From, hello.To, hello.Nonce, challenge.Nonce, binding, "client")
	if !crypto.VerifySignature(crypto.CompressPubkey(remoteKey), hash, response.Signature) {
		return nil, errors.New("bulk auth response signature invalid")
	}
	return peer, nil
}

func (b *BulkSidecar) initiateAuth(conn *quic.Conn, remote *enode.Node) (err error) {
	ctx, cancel := context.WithTimeout(context.Background(), bulkAuthTimeout)
	defer cancel()

	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, stream.Close()) }()
	binding, err := bulkTLSBinding(conn)
	if err != nil {
		return err
	}
	var hello bulkAuthHello
	hello.Version = bulkSidecarVersion
	hello.From = b.localID
	hello.To = remote.ID()
	if _, err := io.ReadFull(crand.Reader, hello.Nonce[:]); err != nil {
		return err
	}
	if err := writeBulkControl(stream, hello); err != nil {
		return err
	}
	var challenge bulkAuthChallenge
	if err := readBulkControl(stream, bulkAuthControlMaxSize, &challenge); err != nil {
		return err
	}
	if len(challenge.Signature) != 64 {
		return errors.New("bulk auth challenge signature length invalid")
	}
	remoteKey := remote.Pubkey()
	if remoteKey == nil {
		return errors.New("bulk auth remote pubkey missing")
	}
	hash := bulkAuthTranscriptHash(hello.From, hello.To, hello.Nonce, challenge.Nonce, binding, "server")
	if !crypto.VerifySignature(crypto.CompressPubkey(remoteKey), hash, challenge.Signature) {
		return errors.New("bulk auth challenge signature invalid")
	}
	hash = bulkAuthTranscriptHash(hello.From, hello.To, hello.Nonce, challenge.Nonce, binding, "client")
	sig, err := crypto.Sign(hash, b.priv)
	if err != nil {
		return err
	}
	return writeBulkControl(stream, bulkAuthResponse{Signature: slices.Clone(sig[:64])})
}

func bulkTLSBinding(conn *quic.Conn) ([]byte, error) {
	state := conn.ConnectionState().TLS
	// RFC 9266 binds the enode proof to this completed TLS 1.3 handshake.
	return state.ExportKeyingMaterial("EXPORTER-Channel-Binding", nil, bulkAuthBindingLength)
}

func bulkAuthTranscriptHash(from, to enode.ID, nonceA, nonceB [32]byte, binding []byte, role string) []byte {
	return crypto.Keccak256(
		[]byte(bulkAuthBindingLabel),
		[]byte(bulkSidecarNextProto),
		[]byte(role),
		binding,
		from[:],
		to[:],
		nonceA[:],
		nonceB[:],
	)
}

func newBulkSidecarVerifiedTLSConfig() *tls.Config {
	return &tls.Config{
		InsecureSkipVerify: true,
		ServerName:         bulkSidecarTLSServerName,
		VerifyConnection:   verifyBulkSidecarTLSConnection,
		NextProtos:         []string{bulkSidecarNextProto},
		MinVersion:         tls.VersionTLS13,
	}
}

func verifyBulkSidecarTLSConnection(state tls.ConnectionState) error {
	if len(state.PeerCertificates) == 0 {
		return errors.New("bulk sidecar tls peer certificate missing")
	}
	cert := state.PeerCertificates[0]
	now := time.Now()
	if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
		return errors.New("bulk sidecar tls peer certificate expired or not yet valid")
	}
	if err := cert.VerifyHostname(bulkSidecarTLSServerName); err != nil {
		return fmt.Errorf("bulk sidecar tls peer certificate name invalid: %w", err)
	}
	if cert.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		return errors.New("bulk sidecar tls peer certificate missing digital signature usage")
	}
	if len(cert.ExtKeyUsage) != 0 && !slices.Contains(cert.ExtKeyUsage, x509.ExtKeyUsageServerAuth) {
		return errors.New("bulk sidecar tls peer certificate missing server auth usage")
	}
	if err := cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature); err != nil {
		return fmt.Errorf("bulk sidecar tls peer certificate signature invalid: %w", err)
	}
	return nil
}

func generateBulkSidecarCertificate() (tls.Certificate, error) {
	return generateBulkSidecarCertificateAt(time.Now())
}

func generateBulkSidecarCertificateAt(now time.Time) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := crand.Int(crand.Reader, serialLimit)
	if err != nil {
		return tls.Certificate{}, err
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(bulkSidecarCertLifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:     []string{bulkSidecarTLSServerName},
	}
	der, err := x509.CreateCertificate(crand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	// Parse the leaf so the rotator can read the validity window without
	// re-parsing on every handshake.
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, nil
}

// Handshake-driven rotation needs no background goroutine. Only the listener
// presents a certificate; the dialer authenticates with the enode transcript.
type bulkCertRotator struct {
	lock sync.Mutex
	cert *tls.Certificate

	// generate is swapped out in tests; nil means generateBulkSidecarCertificateAt.
	generate func(time.Time) (tls.Certificate, error)
}

// certificate implements tls.Config.GetCertificate.
func (r *bulkCertRotator) certificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return r.currentAt(time.Now())
}

func (r *bulkCertRotator) currentAt(now time.Time) (*tls.Certificate, error) {
	r.lock.Lock()
	defer r.lock.Unlock()

	valid := r.cert != nil && !now.Before(r.cert.Leaf.NotBefore) && now.Before(r.cert.Leaf.NotAfter)
	if valid && now.Before(r.cert.Leaf.NotAfter.Add(-bulkSidecarCertRefresh)) {
		return r.cert, nil
	}
	generate := r.generate
	if generate == nil {
		generate = generateBulkSidecarCertificateAt
	}
	cert, err := generate(now)
	if err != nil {
		if !valid {
			return nil, err
		}
		// GetCertificate errors abort the handshake, so keep serving a still
		// valid certificate and retry rotation on the next handshake.
		log.Warn("Bulk sidecar certificate rotation failed, serving previous", "expires", r.cert.Leaf.NotAfter, "err", err)
		return r.cert, nil
	}
	r.cert = &cert
	return r.cert, nil
}
