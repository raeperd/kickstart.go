package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHttp(t *testing.T) {
	const message = "testing heath check message"

	mock := httptest.NewServer(http.HandlerFunc(handleHealthCheck(message)))
	t.Cleanup(mock.Close)

	client := http.Client{}
	response, err := client.Get(mock.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { response.Body.Close() })

	if response.StatusCode != http.StatusOK {
		t.Fatalf("wanted: %d got: %d", http.StatusOK, response.StatusCode)
	}

	want := "text/plain; charset=utf-8"
	got := response.Header.Get("Content-Type")
	if got != want {
		t.Fatalf("wanted: %s got: %s", want, got)
	}

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}

	want = message
	got = string(body)
	if len(want) != len(got) || want != got {
		t.Fatalf("wanted: %s got: %s", message, string(body))
	}

}
