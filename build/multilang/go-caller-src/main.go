// One node of the multi-language acceptance test: an ordinary Go HTTP server
// that calls into the Java chain.
//
// Go has no equivalent of retransforming already-loaded bytecode — there is
// no monkey-patching mechanism for a compiled binary at all, with or without a
// restart — so eBPF observation is not a workaround here the way it is for
// Node or Python: it is the only automatic instrumentation Go has ever had.
// This node contains no tracing code, matching every other node in this test.
package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"
)

func main() {
	port := os.Getenv("CALLER_PORT")
	if port == "" {
		port = "8096"
	}
	downstream := os.Getenv("CALLER_DOWNSTREAM")
	if downstream == "" {
		downstream = "http://127.0.0.1:8081/api/gateway"
	}

	http.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok"))
	})
	http.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		resp, err := http.Get(downstream)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"go":"ok","downstream":%d}`, resp.StatusCode)
	})

	log.Printf("[go-caller] listening on %s downstream=%s pid=%d", port, downstream, os.Getpid())

	// Self-driven, like the Java chain's own gateway: a test relying on
	// someone else to send requests at the right moments is a test with a
	// race condition baked into it, and a chart with one lonely point looks
	// broken even when the pipeline behind it is not.
	if ms, err := strconv.Atoi(os.Getenv("CALLER_SELFLOOP_MS")); err == nil && ms > 0 {
		go selfLoop(port, time.Duration(ms)*time.Millisecond)
	} else if os.Getenv("CALLER_SELFLOOP_MS") == "" {
		go selfLoop(port, 3*time.Second)
	}

	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func selfLoop(port string, interval time.Duration) {
	url := "http://127.0.0.1:" + port + "/"
	for range time.Tick(interval) {
		resp, err := http.Get(url)
		if err != nil {
			log.Printf("[go-caller] self-loop failed: %v", err)
			continue
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		log.Printf("[go-caller] self-loop -> %d", resp.StatusCode)
	}
}
