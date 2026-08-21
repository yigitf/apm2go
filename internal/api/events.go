package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// heartbeatInterval keeps the event stream alive through proxies that close
// idle connections, and lets the browser notice a dead server promptly.
const heartbeatInterval = 20 * time.Second

// Events streams JVM lifecycle changes to the browser over server-sent events.
//
// SSE rather than websockets because the traffic is one-directional and low
// volume: the browser needs to know when a JVM appears, is attached or fails,
// and nothing more. SSE also reconnects on its own, which matters when apm2go
// itself restarts.
func (h *Handlers) Events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is not supported by this server")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Proxies that buffer responses would defeat the whole point of a stream.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	if h.Inventory == nil {
		// Nothing to stream, but keeping the connection open lets a server-mode
		// instance behave the same as a full one from the browser's point of view.
		<-r.Context().Done()
		return
	}

	events, unsubscribe := h.Inventory.Subscribe()
	defer unsubscribe()

	// The stream is a live feed, not a log: a client that just connected gets
	// the current state so it does not have to wait for something to change.
	writeEvent(w, flusher, "snapshot", h.Inventory.List())

	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return

		case ev, ok := <-events:
			if !ok {
				return
			}
			writeEvent(w, flusher, string(ev.Type), ev.Entry)

		case <-heartbeat.C:
			// A comment line is a no-op for the client but keeps the socket warm.
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// writeEvent emits one SSE frame.
func writeEvent(w http.ResponseWriter, flusher http.Flusher, name string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data); err != nil {
		return
	}
	flusher.Flush()
}
