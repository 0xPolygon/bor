package server

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const fakeToken = "Bearer secret"

func TestAuthenticate_MissingMetadata(t *testing.T) {
	t.Parallel()
	// Plain context with no gRPC metadata attached.
	err := authenticate(context.Background(), "secret")
	require.Error(t, err)
	s, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.Unauthenticated, s.Code())
	require.Contains(t, s.Message(), "missing metadata")
}

func TestAuthenticate_MissingAuthorizationHeader(t *testing.T) {
	t.Parallel()
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("other-header", "value"))
	err := authenticate(ctx, "secret")
	require.Error(t, err)
	s, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.Unauthenticated, s.Code())
	require.Contains(t, s.Message(), "missing authorization header")
}

func TestAuthenticate_MissingBearerPrefix(t *testing.T) {
	t.Parallel()
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Basic secret"))
	err := authenticate(ctx, "secret")
	require.Error(t, err)
	s, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.Unauthenticated, s.Code())
	require.Contains(t, s.Message(), "invalid authorization header")
}

func TestAuthenticate_WrongToken(t *testing.T) {
	t.Parallel()
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer wrong_token"))
	err := authenticate(ctx, "secret")
	require.Error(t, err)
	s, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.Unauthenticated, s.Code())
	require.Contains(t, s.Message(), "invalid token")
}

func TestAuthenticate_CorrectToken(t *testing.T) {
	t.Parallel()
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", fakeToken))
	err := authenticate(ctx, "secret")
	require.NoError(t, err)
}

// TestAuthenticate_CaseInsensitiveBearerPrefix verifies the auth scheme name is case-insensitive.
func TestAuthenticate_CaseInsensitiveBearerPrefix(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		hdr  string
	}{
		{"canonical", "Bearer secret"},
		{"lowercase", "bearer secret"},
		{"uppercase", "BEARER secret"},
		{"mixed-case", "BeArEr secret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", tc.hdr))
			require.NoError(t, authenticate(ctx, "secret"))
		})
	}
}

// TestAuthenticate_ConstantTimeCompare verifies that both a close-miss token and
// a completely different token both return Unauthenticated (no behavioral
// difference based on byte position — the unit test checks that both fail).
func TestAuthenticate_ConstantTimeCompare(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		token string
	}{
		{"one-byte-diff", "abd"},
		{"totally-different", "xyz"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+tc.token))
			err := authenticate(ctx, "abc")
			require.Error(t, err)
			s, ok := status.FromError(err)
			require.True(t, ok)
			require.Equal(t, codes.Unauthenticated, s.Code())
		})
	}
}

// TestCombinedUnaryInterceptor tests the unary interceptor's behavior with various token configurations and metadata.
func TestCombinedUnaryInterceptor(t *testing.T) {
	makeSrv := func(token string) *Server {
		return &Server{config: &Config{GRPC: &GRPCConfig{Token: token}}}
	}

	info := &grpc.UnaryServerInfo{FullMethod: "/test/Method"}
	okHandler := func(ctx context.Context, req interface{}) (interface{}, error) { return "ran", nil }

	t.Run("no configured token bypasses auth and runs handler", func(t *testing.T) {
		srv := makeSrv("")
		resp, err := srv.combinedUnaryInterceptor()(context.Background(), nil, info, okHandler)
		require.NoError(t, err)
		require.Equal(t, "ran", resp)
	})

	t.Run("configured token with no client metadata rejects Unauthenticated", func(t *testing.T) {
		srv := makeSrv("secret")
		ran := false
		handler := func(ctx context.Context, req interface{}) (interface{}, error) { ran = true; return nil, nil }
		_, err := srv.combinedUnaryInterceptor()(context.Background(), nil, info, handler)
		require.Error(t, err)
		s, ok := status.FromError(err)
		require.True(t, ok)
		require.Equal(t, codes.Unauthenticated, s.Code())
		require.False(t, ran, "handler must not run when auth is rejected")
	})

	t.Run("configured token with matching bearer runs handler", func(t *testing.T) {
		srv := makeSrv("secret")
		ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", fakeToken))
		resp, err := srv.combinedUnaryInterceptor()(ctx, nil, info, okHandler)
		require.NoError(t, err)
		require.Equal(t, "ran", resp)
	})
}

// TestCombinedStreamInterceptor covers the stream interceptor's behavior with various token configurations and metadata.
func TestCombinedStreamInterceptor(t *testing.T) {
	makeSrv := func(token string) *Server {
		return &Server{config: &Config{GRPC: &GRPCConfig{Token: token}}}
	}

	info := &grpc.StreamServerInfo{FullMethod: "/test/Stream"}

	t.Run("no configured token bypasses auth and runs handler", func(t *testing.T) {
		srv := makeSrv("")
		ran := false
		handler := func(srv interface{}, ss grpc.ServerStream) error { ran = true; return nil }
		err := srv.combinedStreamInterceptor()(nil, &fakeServerStream{ctx: context.Background()}, info, handler)
		require.NoError(t, err)
		require.True(t, ran)
	})

	t.Run("configured token with no metadata rejects Unauthenticated and skips handler", func(t *testing.T) {
		srv := makeSrv("secret")
		ran := false
		handler := func(srv interface{}, ss grpc.ServerStream) error { ran = true; return nil }
		err := srv.combinedStreamInterceptor()(nil, &fakeServerStream{ctx: context.Background()}, info, handler)
		require.Error(t, err)
		s, ok := status.FromError(err)
		require.True(t, ok)
		require.Equal(t, codes.Unauthenticated, s.Code())
		require.False(t, ran, "handler must not run when auth is rejected")
	})

	t.Run("handler error is propagated unchanged", func(t *testing.T) {
		srv := makeSrv("")
		sentinel := errors.New("handler failure sentinel")
		handler := func(srv interface{}, ss grpc.ServerStream) error { return sentinel }
		err := srv.combinedStreamInterceptor()(nil, &fakeServerStream{ctx: context.Background()}, info, handler)
		require.ErrorIs(t, err, sentinel, "interceptor must propagate handler errors, not swallow them")
	})

	t.Run("configured token with matching bearer runs handler", func(t *testing.T) {
		srv := makeSrv("secret")
		ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", fakeToken))
		ran := false
		handler := func(srv interface{}, ss grpc.ServerStream) error { ran = true; return nil }
		err := srv.combinedStreamInterceptor()(nil, &fakeServerStream{ctx: ctx}, info, handler)
		require.NoError(t, err)
		require.True(t, ran)
	})
}

// fakeServerStream is the minimum surface of grpc.ServerStream needed by the
// auth interceptor: it only inspects ss.Context().
type fakeServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f *fakeServerStream) Context() context.Context { return f.ctx }

func TestIsLoopbackHostPort(t *testing.T) {
	t.Parallel()

	cases := []struct {
		hostport string
		want     bool
	}{
		{"127.0.0.1:3131", true},
		{"127.0.0.5:3131", true}, // anywhere in 127.0.0.0/8
		{"[::1]:3131", true},
		{"localhost:3131", true},
		{"LOCALHOST:3131", true}, // hostnames are case-insensitive (RFC 4343)
		{"LocalHost:3131", true},

		{"0.0.0.0:3131", false},
		{"[::]:3131", false},
		{":3131", false}, // wildcard via empty host
		{"10.0.0.1:3131", false},
		{"192.168.1.5:3131", false},
		{"bor.example.net:3131", false}, // unresolved hostname — conservative
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.hostport, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, IsLoopbackHostPort(tc.hostport))
		})
	}
}

// TestWithGRPCAddress_EmptyAddrSkipsStartup verifies that an empty grpc.addr
// is treated as a clean disable — the closure returns nil without trying to
// bind a listener. Other configurations actually bind a listener and are
// covered by the integration-style tests in server_test.go.
func TestWithGRPCAddress_EmptyAddrSkipsStartup(t *testing.T) {
	t.Parallel()
	cfg := &Config{GRPC: &GRPCConfig{Addr: "", Token: ""}}
	// nil Server is safe here — the guard returns before touching it.
	err := WithGRPCAddress()(nil, cfg)
	require.NoError(t, err)
}
