package main

import (
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestHttp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := run(ctx, nil, []string{"testapp", "-port", port()}); err != nil {
			log.Fatalf("%v\n", err)
		}
	}()
	waitForHealthy(ctx, 2*time.Second, endpoint())

	res, err := http.Get(endpoint())
	if err != nil {
		t.Fatal(err)
	}

	if res.StatusCode != http.StatusOK {
		t.Fatalf("wanted: %d got: %d", http.StatusOK, res.StatusCode)
	}

	want := "text/plain; charset=utf-8"
	got := res.Header.Get("Content-Type")
	if got != want {
		t.Fatalf("wanted: %s got: %s", want, got)
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	want = "pong"
	got = string(body)
	if len(want) != len(got) || want != got {
		t.Fatalf("wanted: %s got: %s", want, string(body))
	}

}

func endpoint() string {
	return "http://localhost:" + port()
}

func port() string {
	_portOnce.Do(func() {
		log.Printf("getting a free port")
		listener, err := net.Listen("tcp", ":0")
		if err != nil {
			log.Fatalf("Failed to get a free port: %v", err)
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
