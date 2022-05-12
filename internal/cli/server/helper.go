package server

import (
	"fmt"
	"io/ioutil"
	"math/rand"
	"net"
	"os"
	"sync/atomic"
	"time"
)

var maxPortCheck int32 = 100

// findAvailablePort returns the next available port starting from `from`
func findAvailablePort(from int32, count int32) (int32, error) {
	if count == maxPortCheck {
		return 0, fmt.Errorf("no available port found")
	}
	port := atomic.AddInt32(&from, 1)
	addr := fmt.Sprintf("localhost:%d", port)
	lis, err := net.Listen("tcp", addr)
	count++
	if err == nil {
		lis.Close()
		return port, nil
	} else {
		return findAvailablePort(from, count)
	}
}

func CreateMockServer(config *Config) (*Server, error) {
	if config == nil {
		config = DefaultConfig()
	}

	// find available port
	rand.Seed(time.Now().UnixNano())
	var from int32 = 60000 // the min port to start checking from
	var to int32 = 61000   // the max port to start checking from
	port, err := findAvailablePort(rand.Int31n(to-from+1)+from, 0)
	if err != nil {
		return nil, err
	}

	// grpc port
	config.GRPC.Addr = fmt.Sprintf(":%d", port)

	// datadir
	datadir, _ := ioutil.TempDir("/tmp", "bor-cli-test")
	config.DataDir = datadir

	// start the server
	return NewServer(config)
}

func CloseMockServer(server *Server) {
	// remove the contents of temp data dir
	os.RemoveAll(server.config.DataDir)

	// close the server
	server.Stop()
}
