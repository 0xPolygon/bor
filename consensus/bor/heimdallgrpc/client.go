package heimdallgrpc

import (
	"context"
	"crypto/tls"
	"net"
	"net/url"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/ethereum/go-ethereum/consensus/bor/heimdall"
	"github.com/ethereum/go-ethereum/log"
	grpcRetry "github.com/grpc-ecosystem/go-grpc-middleware/retry"

	borTypes "github.com/0xPolygon/heimdall-v2/x/bor/types"
	checkpointTypes "github.com/0xPolygon/heimdall-v2/x/checkpoint/types"
	clerkTypes "github.com/0xPolygon/heimdall-v2/x/clerk/types"
	milestoneTypes "github.com/0xPolygon/heimdall-v2/x/milestone/types"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
)

const (
	stateFetchLimit = 50
	defaultTimeout  = 30 * time.Second
)

type HeimdallGRPCClient struct {
	conn                  *grpc.ClientConn
	client                *heimdall.HeimdallClient
	borQueryClient        borTypes.QueryClient
	checkpointQueryClient checkpointTypes.QueryClient
	clerkQueryClient      clerkTypes.QueryClient
	milestoneQueryClient  milestoneTypes.QueryClient
}

// NewHeimdallGRPCClient creates a new Heimdall gRPC client with appropriate credentials
// based on the provided address scheme.
func NewHeimdallGRPCClient(grpcAddress string, heimdallURL string, timeout time.Duration) *HeimdallGRPCClient {
	addr := grpcAddress
	var dialOpts []grpc.DialOption

	// URL mode
	if strings.Contains(grpcAddress, "://") {
		// Decide credentials and normalized address based on the provided scheme
		u, err := url.Parse(grpcAddress)
		if err != nil {
			log.Crit("Invalid Heimdall gRPC URL", "url", grpcAddress, "err", err)
		}

		switch u.Scheme {
		case "https":
			// Remote secure connection
			addr = u.Host
			if addr == "" {
				log.Crit("Invalid Heimdall gRPC https URL", "url", grpcAddress)
			}

			tlsCfg := &tls.Config{
				ServerName: strings.Split(addr, ":")[0],
				MinVersion: tls.VersionTLS12,
			}
			dialOpts = append(dialOpts,
				grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
			)

		case "http":
			// plaintext only allowed for local host
			addr = u.Host
			if !isLocalhost(addr) {
				log.Crit("Refusing insecure non-local Heimdall gRPC over http; use https or localhost only",
					"addr", addr)
			}
			dialOpts = append(dialOpts,
				grpc.WithTransportCredentials(insecure.NewCredentials()),
			)

		case "unix":
			// support unix://path for on-box Heimdall nodes
			path := u.Path
			if path == "" {
				log.Crit("Invalid unix Heimdall gRPC URL", "url", grpcAddress)
			}
			addr = "unix://" + path
			dialOpts = append(dialOpts,
				grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
					return net.DialTimeout("unix", strings.TrimPrefix(addr, "unix://"), timeout)
				}),
				grpc.WithTransportCredentials(insecure.NewCredentials()),
			)

		default:
			log.Crit("Unsupported Heimdall gRPC URL scheme", "url", grpcAddress, "scheme", u.Scheme)
		}
	} else {
		addr = grpcAddress
		// No scheme provided, treat as host:port, but only allow if local
		if !isLocalhost(addr) {
			log.Crit("Refusing insecure non-local Heimdall gRPC without scheme; use https://host:port",
				"addr", addr)
		}
		dialOpts = append(dialOpts,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
	}

	// Retry options
	retryOpts := []grpcRetry.CallOption{
		grpcRetry.WithMax(10000),
		grpcRetry.WithBackoff(grpcRetry.BackoffLinear(5 * time.Second)),
		grpcRetry.WithCodes(codes.Internal, codes.Unavailable, codes.Aborted, codes.NotFound),
	}

	dialOpts = append(dialOpts,
		grpc.WithStreamInterceptor(grpcRetry.StreamClientInterceptor(retryOpts...)),
		grpc.WithUnaryInterceptor(grpcRetry.UnaryClientInterceptor(retryOpts...)),
	)

	// dial using address and dialOpts
	conn, err := grpc.NewClient(addr, dialOpts...)
	if err != nil {
		log.Crit("Failed to connect to Heimdall gRPC", "addr", addr, "error", err)
	}

	log.Info("Connected to Heimdall gRPC server", "grpcAddress", grpcAddress, "dialAddr", addr)

	return &HeimdallGRPCClient{
		conn:                  conn,
		client:                heimdall.NewHeimdallClient(heimdallURL, timeout),
		borQueryClient:        borTypes.NewQueryClient(conn),
		checkpointQueryClient: checkpointTypes.NewQueryClient(conn),
		clerkQueryClient:      clerkTypes.NewQueryClient(conn),
		milestoneQueryClient:  milestoneTypes.NewQueryClient(conn),
	}
}

func (h *HeimdallGRPCClient) Close() {
	log.Debug("Shutdown detected, Closing Heimdall gRPC client")
	h.conn.Close()
}

func (h *HeimdallGRPCClient) FetchStatus(ctx context.Context) (*ctypes.SyncInfo, error) {
	return h.client.FetchStatus(ctx)
}

// isLocalhost returns true if host/port refers to localhost/loopback.
func isLocalhost(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
