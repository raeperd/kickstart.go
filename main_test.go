package main

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/carlmjohnson/be"
)

func TestHttp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := run(ctx, os.Stdout, []string{"testapp", "-port", port()}); err != nil {
			log.Fatalf("%v\n", err)
		}
	}()
	waitForHealthy(ctx, 2*time.Second, endpoint()+"/health")

	res, err := http.Get(endpoint() + "/health")
	be.NilErr(t, err)
	be.Equal(t, http.StatusOK, res.StatusCode)
	be.Equal(t, "application/json", res.Header.Get("Content-Type"))

	body := make(map[string]any)
	err = json.NewDecoder(res.Body).Decode(&body)
	be.NilErr(t, err)
	defer res.Body.Close()

	be.Nonzero(t, body["version"])
	be.Nonzero(t, body["vcs.revision"])
	be.Nonzero(t, body["vcs.time"])
	be.Nonzero(t, body["vcs.modified"])

}

func endpoint() string {
	return "http://localhost:" + port()
}

func port() string {
	_portOnce.Do(func() {
		listener, err := net.Listen("tcp", ":0")
		if err != nil {
			log.Fatalf("failed to listen: %v", err)
		}
		defer listener.Close()
		addr := listener.Addr().(*net.TCPAddr)
		_port = strconv.Itoa(addr.Port)
	})
	return _port
}

var (
	_portOnce sync.Once
	_port     string
)

func waitForHealthy(ctx context.Context, timeout time.Duration, endpoint string) {
	startTime := time.Now()
	for {
		res, err := http.Get(endpoint)
		if err == nil && res.StatusCode == http.StatusOK {
			log.Printf("endpoint %s is ready after %v\n", endpoint, time.Since(startTime))
			return
		}

		select {
		case <-ctx.Done():
			return
		default:
			if timeout <= time.Since(startTime) {
				log.Fatalf("timeout %v reached while waitForHealthy", timeout)
				return
			}
			time.Sleep(250 * time.Millisecond)
		}
	}
}
