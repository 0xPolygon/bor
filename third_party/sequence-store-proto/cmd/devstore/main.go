// Command devstore serves the in-memory reference sequence store over
// plaintext gRPC — for tests and devnets only.
package main

import (
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"google.golang.org/grpc"

	"github.com/0xPolygon/sequence-store-proto/devstore"
	pb "github.com/0xPolygon/sequence-store-proto/sequencestore/v1"
)

func main() {
	addr := flag.String("addr", ":7788", "listen address")
	chainID := flag.Uint64("chain-id", 137, "chain id seeding the commitment chain")
	flag.Parse()

	lis, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("listen %s: %v", *addr, err)
	}

	store := devstore.New(*chainID)

	srv := grpc.NewServer()
	pb.RegisterPublisherServiceServer(srv, store)
	pb.RegisterConsumerServiceServer(srv, store)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-stop
		// Stop, not GracefulStop: consumer streams are infinite, so a
		// graceful drain never completes, and an in-memory store has
		// nothing to flush.
		srv.Stop()
	}()

	log.Printf("devstore listening on %s (chain id %d, seed %x)", *addr, *chainID, store.Head())

	if err := srv.Serve(lis); err != nil {
		log.Fatalf("serve: %v", err)
	}
}
