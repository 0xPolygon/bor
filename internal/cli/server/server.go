package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/ethereum/go-ethereum/cmd/utils"
	"github.com/ethereum/go-ethereum/consensus/beacon" //nolint:typecheck
	"github.com/ethereum/go-ethereum/consensus/bor"    //nolint:typecheck
	"github.com/ethereum/go-ethereum/consensus/clique"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/eth/ethconfig"
	"github.com/ethereum/go-ethereum/eth/tracers"
	"github.com/ethereum/go-ethereum/ethstats"
	"github.com/ethereum/go-ethereum/graphql"
	"github.com/ethereum/go-ethereum/internal/cli/server/pprof"
	"github.com/ethereum/go-ethereum/internal/cli/server/proto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/metrics/influxdb"
	"github.com/ethereum/go-ethereum/metrics/prometheus"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/hellofresh/health-go/v5"
	"github.com/mattn/go-colorable"
	"github.com/mattn/go-isatty"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.4.0"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	// Force-load the tracer engines to trigger registration
	_ "github.com/ethereum/go-ethereum/eth/tracers/js"
	_ "github.com/ethereum/go-ethereum/eth/tracers/native"

	protobor "github.com/0xPolygon/polyproto/bor"
)

type Server struct {
	proto.UnimplementedBorServer
	protobor.UnimplementedBorApiServer

	node       *node.Node
	backend    *eth.Ethereum
	grpcServer *grpc.Server
	tracer     *sdktrace.TracerProvider
	config     *Config

	// tracerAPI to trace block executions
	tracerAPI *tracers.API

	// Bor health service.
	healthService *health.Health
}

type serverOption func(srv *Server, config *Config) error

var glogger *log.GlogHandler

func init() {
	handler := log.NewTerminalHandlerWithLevel(os.Stderr, log.LevelInfo, false)
	log.SetDefault(log.NewLogger(handler))
}

func WithGRPCAddress() serverOption {
	return func(srv *Server, config *Config) error {
		return srv.gRPCServerByAddress(config.GRPC.Addr)
	}
}

func WithGRPCListener(lis net.Listener) serverOption {
	return func(srv *Server, _ *Config) error {
		return srv.gRPCServerByListener(lis)
	}
}

func VerbosityIntToString(verbosity int) string {
	mapIntToString := map[int]string{
		5: "trace",
		4: "debug",
		3: "info",
		2: "warn",
		1: "error",
		0: "crit",
	}

	return mapIntToString[verbosity]
}

func VerbosityStringToInt(loglevel string) int {
	mapStringToInt := map[string]int{
		"trace": 5,
		"debug": 4,
		"info":  3,
		"warn":  2,
		"error": 1,
		"crit":  0,
	}

	return mapStringToInt[loglevel]
}

//nolint:gocognit
func NewServer(config *Config, opts ...serverOption) (*Server, error) {
	// Enable metric collection if requested
	if err := setupMetrics(config.Telemetry); err != nil {
		return nil, err
	}

	// start pprof
	if config.Pprof.Enabled {
		pprof.SetMemProfileRate(config.Pprof.MemProfileRate)
		pprof.SetSetBlockProfileRate(config.Pprof.BlockProfileRate)
		pprof.StartPProf(fmt.Sprintf("%s:%d", config.Pprof.Addr, config.Pprof.Port))
	}

	runtime.SetMutexProfileFraction(5)

	srv := &Server{
		config: config,
	}

	// start the logger
	setupLogger(config.Verbosity, *config.Logging)

	// setup open telemetry collector
	srv.setupOpenCollector()

	var err error

	for _, opt := range opts {
		err = opt(srv, config)
		if err != nil {
			return nil, err
		}
	}

	// load the chain genesis
	if err = config.loadChain(); err != nil {
		return nil, err
	}

	// create the node/stack
	nodeCfg, err := config.buildNode()
	if err != nil {
		return nil, err
	}

	stack, err := node.New(nodeCfg)
	if err != nil {
		return nil, err
	}

	// setup account manager (only keystore)
	// create a new account manager, only for the scope of this function
	accountManager := accounts.NewManager(&accounts.Config{})

	// register backend to account manager with keystore for signing
	keydir := stack.KeyStoreDir()

	n, p := keystore.StandardScryptN, keystore.StandardScryptP
	if config.Accounts.UseLightweightKDF {
		n, p = keystore.LightScryptN, keystore.LightScryptP
	}

	// proceed to authorize the local account manager in any case
	accountManager.AddBackend(keystore.NewKeyStore(keydir, n, p))

	// flag to set if we're authorizing consensus here
	authorized := false

	var ethCfg *ethconfig.Config

	// check if personal wallet endpoints are disabled or not
	// nolint:nestif
	if !config.Accounts.DisableBorWallet {
		// add keystore globally to the node's account manager if personal wallet is enabled
		stack.AccountManager().AddBackend(keystore.NewKeyStore(keydir, n, p))

		// register the ethereum backend
		ethCfg, err = config.buildEth(stack, stack.AccountManager())
		if err != nil {
			return nil, err
		}

		backend, err := eth.New(stack, ethCfg)
		if err != nil {
			return nil, err
		}

		srv.backend = backend
	} else {
		// register the ethereum backend (with temporary created account manager)
		ethCfg, err = config.buildEth(stack, accountManager)
		if err != nil {
			return nil, err
		}

		backend, err := eth.New(stack, ethCfg)
		if err != nil {
			return nil, err
		}

		srv.backend = backend

		// authorize only if mining or in developer mode
		if config.Sealer.Enabled || config.Developer.Enabled {
			// get the etherbase
			eb, err := srv.backend.Etherbase()
			if err != nil {
				log.Error("Cannot start mining without etherbase", "err", err)

				return nil, fmt.Errorf("etherbase missing: %v", err)
			}

			// Authorize the clique consensus (if chosen) to sign using wallet signer
			var cli *clique.Clique
			if c, ok := srv.backend.Engine().(*clique.Clique); ok {
				cli = c
			} else if cl, ok := srv.backend.Engine().(*beacon.Beacon); ok {
				if c, ok := cl.InnerEngine().(*clique.Clique); ok {
					cli = c
				}
			}

			if cli != nil {
				wallet, err := accountManager.Find(accounts.Account{Address: eb})
				if wallet == nil || err != nil {
					log.Error("Etherbase account unavailable locally", "err", err)
					return nil, fmt.Errorf("signer missing: %v", err)
				}

				cli.Authorize(eb, wallet.SignData)

				authorized = true
			}

			// Authorize the bor consensus (if chosen) to sign using wallet signer
			if bor, ok := srv.backend.Engine().(*bor.Bor); ok {
				wallet, err := accountManager.Find(accounts.Account{Address: eb})
				if wallet == nil || err != nil {
					log.Error("Etherbase account unavailable locally", "err", err)
					return nil, fmt.Errorf("signer missing: %v", err)
				}

				bor.Authorize(eb, wallet.SignData)

				authorized = true
			}
		}
	}

	// set the auth status in backend
	srv.backend.SetAuthorized(authorized)

	filterSystem := utils.RegisterFilterAPI(stack, srv.backend.APIBackend, ethCfg)

	// debug tracing is enabled by default
	stack.RegisterAPIs(tracers.APIs(srv.backend.APIBackend))
	srv.tracerAPI = tracers.NewAPI(srv.backend.APIBackend)

	// graphql is started from another place
	if config.JsonRPC.Graphql.Enabled {
		if err := graphql.New(stack, srv.backend.APIBackend, filterSystem, config.JsonRPC.Graphql.Cors, config.JsonRPC.Graphql.VHost); err != nil {
			return nil, fmt.Errorf("failed to register the GraphQL service: %v", err)
		}
	}

	// register ethash service
	if config.Ethstats != "" {
		if err := ethstats.New(stack, srv.backend.APIBackend, srv.backend.Engine(), config.Ethstats); err != nil {
			return nil, err
		}
	}

	// sealing (if enabled) or in dev mode
	if config.Sealer.Enabled || config.Developer.Enabled {
		// stateless node can't mine
		if config.SyncMode != "stateless" {
			if err := srv.backend.StartMining(); err != nil {
				return nil, err
			}
		} else {
			log.Warn("Mining disabled due to stateless configuration")
		}
	}

	// Set the node instance
	srv.node = stack

	// Register the health service endpoint.
	srv.registerHealthServiceEndpoint()

	// start the node
	if err := srv.node.Start(); err != nil {
		return nil, err
	}

	// start the GRPC Server
	if err := WithGRPCAddress()(srv, config); err != nil {
		return nil, err
	}

	return srv, nil
}

func (s *Server) Stop() {
	if s.node != nil {
		s.node.Close()
	}

	if s.grpcServer != nil {
		s.grpcServer.Stop()
	}

	// shutdown the tracer
	if s.tracer != nil {
		if err := s.tracer.Shutdown(context.Background()); err != nil {
			log.Error("Failed to shutdown open telemetry tracer")
		}
	}
}

func setupMetrics(config *TelemetryConfig) error {
	if !config.Enabled {
		return nil
	}
	if config.Expensive {
		log.Warn("Expensive metrics are collected by default, please remove this flag", "flag", "--metrics.expensive")
	}

	log.Info("Enabling metrics collection")
	metrics.Enable()

	// influxdb
	if v1Enabled, v2Enabled := config.InfluxDB.V1Enabled, config.InfluxDB.V2Enabled; v1Enabled || v2Enabled {
		if v1Enabled && v2Enabled {
			return fmt.Errorf("both influx v1 and influx v2 cannot be enabled")
		}

		cfg := config.InfluxDB
		tags := cfg.Tags
		endpoint := cfg.Endpoint

		if v1Enabled {
			log.Info("Enabling metrics export to InfluxDB (v1)")

			go influxdb.InfluxDBWithTags(metrics.DefaultRegistry, 10*time.Second, endpoint, cfg.Database, cfg.Username, cfg.Password, "geth.", tags)
		}

		if v2Enabled {
			log.Info("Enabling metrics export to InfluxDB (v2)")

			go influxdb.InfluxDBV2WithTags(metrics.DefaultRegistry, 10*time.Second, endpoint, cfg.Token, cfg.Bucket, cfg.Organization, "geth.", tags)
		}
	}

	// Start system runtime metrics collection
	go metrics.CollectProcessMetrics(3 * time.Second)

	if config.PrometheusAddr != "" {
		prometheusMux := http.NewServeMux()

		prometheusMux.Handle("/debug/metrics/prometheus", prometheus.Handler(metrics.DefaultRegistry))

		timeouts := rpc.DefaultHTTPTimeouts

		promServer := &http.Server{
			Addr:              config.PrometheusAddr,
			Handler:           prometheusMux,
			ReadTimeout:       timeouts.ReadTimeout,
			ReadHeaderTimeout: timeouts.ReadHeaderTimeout,
			WriteTimeout:      timeouts.WriteTimeout,
			IdleTimeout:       timeouts.IdleTimeout,
		}

		go func() {
			if err := promServer.ListenAndServe(); err != nil {
				log.Error("Failure in running Prometheus server", "err", err)
			}
		}()

		log.Info("Enabling metrics export to prometheus", "path", fmt.Sprintf("http://%s/debug/metrics/prometheus", config.PrometheusAddr))
	}

	return nil
}

func (s *Server) setupOpenCollector() error {
	config := s.config

	if config.Telemetry.OpenCollectorEndpoint != "" {
		// setup open collector tracer
		ctx := context.Background()

		res, err := resource.New(ctx,
			resource.WithAttributes(
				// the service name used to display traces in backends
				semconv.ServiceNameKey.String(config.Identity),
			),
		)
		if err != nil {
			return fmt.Errorf("failed to create open telemetry resource for service: %v", err)
		}

		// Set up a trace exporter
		traceExporter, err := otlptracegrpc.New(
			ctx,
			otlptracegrpc.WithInsecure(),
			otlptracegrpc.WithEndpoint(config.Telemetry.OpenCollectorEndpoint),
		)
		if err != nil {
			return fmt.Errorf("failed to create open telemetry tracer exporter for service: %v", err)
		}

		// Register the trace exporter with a TracerProvider, using a batch
		// span processor to aggregate spans before export.
		bsp := sdktrace.NewBatchSpanProcessor(traceExporter)
		tracerProvider := sdktrace.NewTracerProvider(
			sdktrace.WithSampler(sdktrace.AlwaysSample()),
			sdktrace.WithResource(res),
			sdktrace.WithSpanProcessor(bsp),
		)
		otel.SetTracerProvider(tracerProvider)

		// set global propagator to tracecontext (the default is no-op).
		otel.SetTextMapPropagator(propagation.TraceContext{})

		// set the tracer
		s.tracer = tracerProvider

		log.Info("Open collector tracing started", "address", config.Telemetry.OpenCollectorEndpoint)
	}
	return nil
}

func (s *Server) gRPCServerByAddress(addr string) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	return s.gRPCServerByListener(lis)
}

func (s *Server) gRPCServerByListener(listener net.Listener) error {
	s.grpcServer = grpc.NewServer(s.withLoggingUnaryInterceptor())
	proto.RegisterBorServer(s.grpcServer, s)
	protobor.RegisterBorApiServer(s.grpcServer, s)
	reflection.Register(s.grpcServer)

	go func() {
		if err := s.grpcServer.Serve(listener); err != nil {
			log.Error("failed to serve grpc server", "err", err)
		}
	}()

	log.Info("GRPC Server started", "addr", listener.Addr())

	return nil
}

func (s *Server) withLoggingUnaryInterceptor() grpc.ServerOption {
	return grpc.UnaryInterceptor(s.loggingServerInterceptor)
}

func (s *Server) loggingServerInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	start := time.Now()
	h, err := handler(ctx, req)

	log.Trace("Request", "method", info.FullMethod, "duration", time.Since(start), "error", err)

	return h, err
}

func setupLogger(logLevel int, loggingInfo LoggingConfig) {
	output := io.Writer(os.Stderr)

	if loggingInfo.Json {
		glogger = log.NewGlogHandler(log.JSONHandler(os.Stderr))
	} else {
		usecolor := (isatty.IsTerminal(os.Stderr.Fd()) || isatty.IsCygwinTerminal(os.Stderr.Fd())) && os.Getenv("TERM") != "dumb"
		if usecolor {
			output = colorable.NewColorableStderr()
		}

		glogger = log.NewGlogHandler(log.NewTerminalHandler(output, usecolor))
	}

	// logging
	lvl := log.FromLegacyLevel(logLevel)
	glogger.Verbosity(lvl)

	if loggingInfo.Vmodule != "" {
		if err := glogger.Vmodule(loggingInfo.Vmodule); err != nil {
			log.Error("failed to set Vmodule", "err", err)
		}
	}

	log.SetDefault(log.NewLogger(glogger))
}

func (s *Server) GetLatestBlockNumber() *big.Int {
	return s.backend.BlockChain().CurrentBlock().Number
}

func (s *Server) GetGrpcAddr() string {
	return s.config.GRPC.Addr[1:]
}

// setupHealthService initializes the health service for Bor.
func (s *Server) setupHealthService() error {
	h, err := health.New(
		health.WithComponent(health.Component{
			Name:    "bor",
			Version: params.VersionWithMeta,
		}),
		health.WithSystemInfo(),
	)
	if err != nil {
		return fmt.Errorf("failed to create health service: %w", err)
	}
	s.healthService = h
	return nil
}

// getBorInfo returns Bor-specific information.
func (s *Server) getBorInfo() map[string]any {
	borInfo := map[string]any{}

	if s.config != nil {
		borInfo["chain_id"] = s.config.chain.Genesis.Config.ChainID.Uint64()
	}

	if s.backend != nil {
		currentBlock := s.backend.BlockChain().CurrentBlock()
		borInfo["latest_block_hash"] = currentBlock.Hash().Hex()
		borInfo["latest_block_number"] = currentBlock.Number.Uint64()
		borInfo["latest_block_timestamp"] = time.Unix(int64(currentBlock.Time), 0).UTC().Format(time.RFC3339Nano)
		borInfo["peer_count"] = s.backend.PeerCount()
		borInfo["sync_mode"] = s.backend.SyncMode()

		if s.backend.Synced() {
			borInfo["catching_up"] = "false"
		} else {
			borInfo["catching_up"] = "true"
		}
	}

	return borInfo
}

func (s *Server) performHealthChecks(healthResponse map[string]any) HealthStatus {
	overallStatus := StatusOK
	var statusMessages []string

	if s.config == nil || s.config.Health == nil {
		return HealthStatus{
			Level:   StatusOK,
			Code:    StatusOK.Code(),
			Message: "",
		}
	}

	// Goroutines check.
	if system, ok := healthResponse["system"].(map[string]any); ok && system != nil {
		if goroutinesCount, ok := system["goroutines_count"].(float64); ok {
			// Check critical threshold first.
			if s.config.Health.MaxGoRoutineThreshold != 0 && int(goroutinesCount) > s.config.Health.MaxGoRoutineThreshold {
				overallStatus = StatusCritical
				statusMessages = append(statusMessages, "number of goroutines above the maximum threshold")
			} else if s.config.Health.WarnGoRoutineThreshold != 0 && int(goroutinesCount) > s.config.Health.WarnGoRoutineThreshold {
				// Only set to warn if we haven't already hit critical.
				if overallStatus != StatusCritical {
					overallStatus = StatusWarn
				}
				statusMessages = append(statusMessages, "number of goroutines above the warning threshold")
			}
		}
	}

	// Peer check - only perform if node_info exists and has peer_count.
	if borInfo, ok := healthResponse["node_info"].(map[string]any); ok && borInfo != nil {
		if peerCount, ok := borInfo["peer_count"].(int); ok {
			// Check critical threshold first.
			if s.config.Health.MinPeerThreshold != 0 && peerCount < s.config.Health.MinPeerThreshold {
				overallStatus = StatusCritical
				statusMessages = append(statusMessages, "number of peers below the minimum threshold")
			} else if s.config.Health.WarnPeerThreshold != 0 && peerCount < s.config.Health.WarnPeerThreshold {
				// Only set to warn if we haven't already hit critical.
				if overallStatus != StatusCritical {
					overallStatus = StatusWarn
				}
				statusMessages = append(statusMessages, "number of peers below the warning threshold")
			}
		}
	}

	return HealthStatus{
		Level:   overallStatus,
		Code:    overallStatus.Code(),
		Message: strings.Join(statusMessages, ", "),
	}
}

// customHealthServiceHandler wraps the health-go handler and adds Bor-specific information on top of it.
func (s *Server) customHealthServiceHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := &ResponseRecorder{
			ResponseWriter: w,
			body:           make([]byte, 0),
		}

		s.healthService.Handler().ServeHTTP(recorder, r)

		var healthResponse map[string]any
		if err := json.Unmarshal(recorder.body, &healthResponse); err != nil {
			log.Error("Failed to unmarshal response: %v\n", err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(recorder.statusCode)
			if _, writeErr := w.Write(recorder.body); writeErr != nil {
				log.Error("Failed to write fallback response: %v\n", writeErr)
			}
			return
		}

		// Remove the "status" field from health-go as it's always "OK" and not useful.
		delete(healthResponse, "status")

		healthResponse["node_info"] = s.getBorInfo()

		status := s.performHealthChecks(healthResponse)
		healthResponse["status"] = status

		healthResponse["error"] = false
		healthResponse["error_message"] = ""

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(recorder.statusCode)

		if err := json.NewEncoder(w).Encode(healthResponse); err != nil {
			log.Error("Failed to encode enhanced health response", "error", err)
			if _, writeErr := w.Write(recorder.body); writeErr != nil {
				log.Error("Failed to write fallback response: %v\n", writeErr)
			}
		}
	})
}

// registerHealthServiceEndpoint registers the /health service endpoint.
func (s *Server) registerHealthServiceEndpoint() {
	if err := s.setupHealthService(); err != nil {
		log.Error("Failed to setup health service", "error", err)
		return
	}
	s.node.RegisterHandler("Health service endpoint", "/health", s.customHealthServiceHandler())
}
