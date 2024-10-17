package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestGetHealth tests the /health endpoint.
// Server is started by [TestMain] so that the test can make requests to it.
func TestGetHealth(t *testing.T) {
	// response is repeated but this describes intention of test better.
	// For example you can add fiels only needed for testing.
	type response struct {
		Version  string    `json:"version"`
		Revision string    `json:"vcs.revision"`
		Time     time.Time `json:"vcs.time"`
		// Modified bool      `json:"vcs.modified"`
	}

	// actual http request to the server.
	res, err := http.Get(endpoint() + "/health")
	testNil(t, err)
	testEqual(t, http.StatusOK, res.StatusCode)
	testEqual(t, "application/json", res.Header.Get("Content-Type"))
	testNil(t, json.NewDecoder(res.Body).Decode(&response{}))
	defer res.Body.Close()
}

// TestGetOpenapi tests the /openapi.yaml endpoint.
// You can add more test as needed without starting the server again.
func TestGetOpenapi(t *testing.T) {
	res, err := http.Get(endpoint() + "/openapi.yaml")
	testNil(t, err)
	testEqual(t, http.StatusOK, res.StatusCode)
	testEqual(t, "text/plain", res.Header.Get("Content-Type"))

	sb := strings.Builder{}
	_, err = io.Copy(&sb, res.Body)
	testNil(t, err)
	res.Body.Close()

	testContains(t, "openapi: 3.1.0", sb.String())
	testContains(t, "version: "+version, sb.String())
}

// TestMain starts the server and runs all the tests.
// By doing this, you can run **actual** integration tests without starting the server.
func TestMain(m *testing.M) {
	flag.Parse() // NOTE: this is needed to parse args from go test command

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	args := []string{"testapp", "--port", port()}
	go func() {
		err := run(ctx, os.Stdout, args, version)
		if err != nil {
			log.Fatalf("failed to run with args %v got err %s\n", args, err)
		}
	}()
	waitForHealthy(ctx, 2*time.Second, endpoint()+"/health")

	os.Exit(m.Run())
}

const version = "test-version"

// endpoint returns the server endpoint started by [TestMain].
func endpoint() string {
	return "http://localhost:" + port()
}

// port returns the port on which the server is started by [TestMain].
// on the first call, it starts a listener on a random port and returns the port.
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

// this variables is not intended to be used directly. use [port] function instead.
var (
	_portOnce sync.Once
	_port     string
)

// waitForHealthy waits for the server to be healthy.
// this function is used by [TestMain] to wait for the server to be healthy before running tests.
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

func testEqual[T comparable](t testing.TB, want, got T) {
	t.Helper()
	if want != got {
		t.Fatalf("want: %v; got: %v", want, got)
	}
}

func testNil(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("got: %v", err)
	}
}

func testContains(t testing.TB, needle string, haystack string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("%q not in %q", needle, haystack)
	}
}
