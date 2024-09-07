package main

import "net/http"

func route() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/health", handleGetHealth())
	mux.Handle("/debug/", handleGetDebug())
	return mux
}
