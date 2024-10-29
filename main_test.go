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
	"testing"
	"time"
)

// TestMain starts the server and runs all the tests.
// By doing this, you can run **actual** integration tests without starting the server.
func TestMain(m *testing.M) {
	flag.Parse() // NOTE: this is needed to parse args from go test command

	port := func() string { // Get a free port to run the server
		listener, err := net.Listen("tcp", ":0")
		if err != nil {
			log.Fatalf("failed to listen: %v", err)
		}
		defer listener.Close()
		addr := listener.Addr().(*net.TCPAddr)
		return strconv.Itoa(addr.Port)
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { // Start the server in a goroutine
		if err := run(ctx, os.Stdout, []string{"test", "--port", port}, "vtest"); err != nil {
			log.Fatal(err)
		}
	}()

	endpoint = "http://localhost:" + port

	start := time.Now() // wait for server to be healthy before tests.
	for time.Since(start) < 3*time.Second {
		if res, err := http.Get(endpoint + "/health"); err == nil && res.StatusCode == http.StatusOK {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}

	os.Exit(m.Run())
}

// endpoint holds the server endpoint started by TestMain, not intended to be updated.
var endpoint string

// TestGetHealth tests the /health endpoint.
// Server is started by [TestMain] so that the test can make requests to it.
func TestGetHealth(t *testing.T) {
	t.Parallel()
	// response is repeated but this describes intention of test better.
	// For example you can add fiels only needed for testing.
	type response struct {
		Version  string    `json:"version"`
		Revision string    `json:"vcs.revision"`
		Time     time.Time `json:"vcs.time"`
		// Modified bool      `json:"vcs.modified"`
	}

	// actual http request to the server.
	res, err := http.Get(endpoint + "/health")
	testNil(t, err)
	testEqual(t, http.StatusOK, res.StatusCode)
	testEqual(t, "application/json", res.Header.Get("Content-Type"))
	testNil(t, json.NewDecoder(res.Body).Decode(&response{}))
	defer res.Body.Close()
}

// TestGetOpenapi tests the /openapi.yaml endpoint.
// You can add more test as needed without starting the server again.
func TestGetOpenapi(t *testing.T) {
	t.Parallel()
	res, err := http.Get(endpoint + "/openapi.yaml")
	testNil(t, err)
	testEqual(t, http.StatusOK, res.StatusCode)
	testEqual(t, "text/plain", res.Header.Get("Content-Type"))

	sb := strings.Builder{}
	_, err = io.Copy(&sb, res.Body)
	testNil(t, err)
	res.Body.Close()

	testContains(t, "openapi: 3.1.0", sb.String())
	testContains(t, "version: ", sb.String())
}

func testEqual[T comparable](t testing.TB, want, got T) {
	t.Helper()
	if want != got {
		t.Fatalf("want: %v; got: %v", want, got)
	}
}

func testNil(t testing.TB, err error) {
	t.Helper()
	testEqual(t, nil, err)
}

func testContains(t testing.TB, needle string, haystack string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("%q not in %q", needle, haystack)
	}
}
