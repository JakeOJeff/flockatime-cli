package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"snapshot-agent/internal/snapshot"
)

// cmdDevServer runs a throwaway collection endpoint so the agent can be tried
// end to end without a real backend. It prints a summary of every batch and
// stores nothing. Not part of the shipped agent's job --- it exists so anyone
// can watch the thing work on their own machine.
func cmdDevServer(args []string) error {
	addr := "127.0.0.1:8787"
	if len(args) > 0 {
		addr = args[0]
		if !strings.Contains(addr, ":") {
			addr = "127.0.0.1:" + addr
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/snapshots", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusOK) // doctor's reachability probe
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}
		var batch []snapshot.Snapshot
		if err := json.Unmarshal(body, &batch); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}

		auth := r.Header.Get("Authorization")
		fmt.Printf("\n--- batch of %d, %d bytes, auth %q\n", len(batch), len(body), mask(auth))
		for _, s := range batch {
			state := fmt.Sprintf("%d files, %d lines", s.FileCount, s.TotalLines)
			if s.Unchanged {
				state += ", unchanged (no file list)"
			}
			git := ""
			if s.Git != nil {
				git = fmt.Sprintf(", git %s@%s dirty=%v ahead=%d", s.Git.Branch, short(s.Git.Head), s.Git.Dirty, s.Git.Ahead)
			}
			fmt.Printf("  %-16s captured_at=%d tree=%s %s%s\n", s.Project, s.CapturedAt, short(s.TreeHash), state, git)
		}
		w.WriteHeader(http.StatusAccepted)
	})

	// Explicit timeouts. A default http.Server has none, so one stalled
	// client holds a connection open forever; ReadHeaderTimeout is what
	// bounds a slow-header client specifically.
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	fmt.Fprintf(os.Stderr, "devserver listening on http://%s (POST /v1/snapshots)\n", addr)
	return srv.ListenAndServe()
}
