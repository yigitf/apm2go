package api

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
)

// statusRecorder captures the response code for the access log.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// Flush forwards to the underlying writer so the event stream keeps working
// through this wrapper.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// withLogging records API requests at debug level, and failures at warn.
//
// Successful requests are logged at debug because the UI polls: logging them at
// info would bury everything else within minutes.
func withLogging(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The event stream never ends, so logging its duration is meaningless.
		if strings.HasSuffix(r.URL.Path, "/events") {
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)

		elapsed := time.Since(start)
		attrs := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"took", elapsed.Round(time.Millisecond),
		}
		if rec.status >= 500 {
			log.Warn("request failed", attrs...)
		} else {
			log.Debug("request served", attrs...)
		}
	})
}

// withRecovery keeps a panic in one handler from taking the process down. An
// APM that crashes while the operator is diagnosing an incident is worse than
// one that returns a 500.
func withRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				// http.ErrAbortHandler is the documented way for a handler to
				// abandon a response; it is not a bug.
				if recovered == http.ErrAbortHandler {
					panic(recovered)
				}
				slog.Error("panic serving request",
					"path", r.URL.Path,
					"panic", recovered,
					"stack", string(debug.Stack()))
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
