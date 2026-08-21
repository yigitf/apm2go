package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/apm2go/apm2go/internal/assets"
	"github.com/apm2go/apm2go/internal/config"
	"github.com/apm2go/apm2go/internal/container"
	"github.com/apm2go/apm2go/internal/hostmetrics"
	"github.com/apm2go/apm2go/internal/inventory"
	"github.com/apm2go/apm2go/internal/jvmdiag"
	"github.com/apm2go/apm2go/internal/model"
	"github.com/apm2go/apm2go/internal/pipeline"
	"github.com/apm2go/apm2go/internal/receiver"
	"github.com/apm2go/apm2go/internal/store"
	"github.com/apm2go/apm2go/internal/version"
)

// Handlers implements the JSON API.
//
// The inventory, pipeline and receiver are optional: in the phase-3 split a
// server-mode process stores and displays data without owning any JVMs, and its
// endpoints report that rather than failing.
type Handlers struct {
	Store     *store.Store
	Inventory *inventory.Manager
	Pipeline  *pipeline.Pipeline
	Receiver  *receiver.Receiver
	// Diagnostics runs on-demand jcmd commands. It is set wherever discovery
	// runs, since it uses the same attach channel as injection.
	Diagnostics *jvmdiag.Client
	// Processes and Containers back /processes: what apm2go is watching through
	// eBPF right now, and who those containers are. Both are nil in a
	// server-mode instance, which watches nothing locally.
	Processes  WatchedProcesses
	Containers *container.Resolver
	Config     config.Config
	Log        *slog.Logger

	startedAt time.Time
}

// NewHandlers returns the API handlers.
func NewHandlers(cfg config.Config, log *slog.Logger) *Handlers {
	return &Handlers{Config: cfg, Log: log, startedAt: time.Now()}
}

// Health reports that the process is up. It deliberately does not touch the
// database: a health check that fails during a slow query would restart apm2go
// exactly when it is busiest.
func (h *Handlers) Health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"version":    version.Version,
		"mode":       h.Config.Mode,
		"uptime_sec": int64(time.Since(h.startedAt).Seconds()),
	})
}

// Self reports apm2go's own resource use and ingest counters, which is what an
// operator checks when traces stop arriving.
func (h *Handlers) Self(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{
		"version":     version.String(),
		"mode":        h.Config.Mode,
		"started_at":  h.startedAt,
		"uptime_sec":  int64(time.Since(h.startedAt).Seconds()),
		"otel_agent":  assets.OtelAgentVersion,
		"config_hint": configSummary(h.Config),
	}
	if h.Receiver != nil {
		out["receiver"] = h.Receiver.Stats()
	}
	if h.Pipeline != nil {
		out["pipeline"] = h.Pipeline.Stats()
		services, operations := h.Pipeline.Cardinality()
		out["cardinality"] = map[string]int{"services": services, "operations": operations}
	}
	if h.Inventory != nil {
		out["container_sources"] = h.Inventory.ContainerSources()
	}
	if h.Store != nil {
		stats, err := h.Store.Stats(r.Context())
		if err != nil {
			h.Log.Warn("could not read storage stats", "error", err)
		} else {
			out["storage"] = stats
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// configSummary exposes the settings an operator most often needs to confirm,
// without dumping a configuration that may hold credentials.
func configSummary(cfg config.Config) map[string]any {
	return map[string]any{
		"auto_attach":      cfg.Attach.AutoAttach,
		"sample_ratio":     cfg.Attach.SampleRatio,
		"span_retention":   cfg.Storage.SpanRetention.String(),
		"rollup_retention": cfg.Storage.RollupRetention.String(),
		"max_spans_per_s":  cfg.Pipeline.MaxSpansPerSecond,
		"otlp_grpc":        cfg.Receiver.GRPCAddr,
		"otlp_http":        cfg.Receiver.HTTPAddr,
	}
}

// ---------------------------------------------------------------- JVMs

// ListJVMs returns every JVM apm2go is tracking.
func (h *Handlers) ListJVMs(w http.ResponseWriter, _ *http.Request) {
	if h.Inventory == nil {
		writeJSON(w, http.StatusOK, []*inventory.Entry{})
		return
	}
	writeJSON(w, http.StatusOK, h.Inventory.List())
}

// GetJVM returns one tracked JVM by pid.
func (h *Handlers) GetJVM(w http.ResponseWriter, r *http.Request) {
	pid, ok := h.requirePID(w, r)
	if !ok {
		return
	}
	entry, err := h.Inventory.Get(pid)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

// AttachJVM instruments a JVM on request.
func (h *Handlers) AttachJVM(w http.ResponseWriter, r *http.Request) {
	pid, ok := h.requirePID(w, r)
	if !ok {
		return
	}

	// Attaching can take seconds, but the browser is waiting, so bound it well
	// under any reasonable client timeout.
	ctx, cancel := contextWithTimeout(r, 60*time.Second)
	defer cancel()

	entry, err := h.Inventory.Attach(ctx, pid)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	// A failed attach is still a valid answer: the entry carries the reason,
	// which is what the UI shows. Only a missing process is an API error.
	writeJSON(w, http.StatusOK, entry)
}

// DisableJVM stops apm2go from attaching to a process.
func (h *Handlers) DisableJVM(w http.ResponseWriter, r *http.Request) {
	h.setManualOnly(w, r, true)
}

// EnableJVM re-enables automatic attaching for a process.
func (h *Handlers) EnableJVM(w http.ResponseWriter, r *http.Request) {
	h.setManualOnly(w, r, false)
}

func (h *Handlers) setManualOnly(w http.ResponseWriter, r *http.Request, manual bool) {
	pid, ok := h.requirePID(w, r)
	if !ok {
		return
	}
	entry, err := h.Inventory.SetManualOnly(pid, manual)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

// requirePID parses the pid path parameter and checks the inventory exists.
func (h *Handlers) requirePID(w http.ResponseWriter, r *http.Request) (int, bool) {
	if h.Inventory == nil {
		writeError(w, http.StatusNotImplemented,
			"this apm2go instance does not manage JVMs (mode is not 'all' or 'agent')")
		return 0, false
	}
	return parsePID(w, r)
}

// parsePID reads the pid path parameter without requiring an inventory.
//
// Endpoints that only read what is already stored use this: a server-mode
// process owns no JVMs but still holds their dumps, and refusing to show them
// would make the stored history unreachable exactly where it lives.
func parsePID(w http.ResponseWriter, r *http.Request) (int, bool) {
	pid, err := strconv.Atoi(r.PathValue("pid"))
	if err != nil || pid <= 0 {
		writeError(w, http.StatusBadRequest, "invalid pid")
		return 0, false
	}
	return pid, true
}

// ---------------------------------------------------------------- traces

// ListServices returns RED metrics per service.
func (h *Handlers) ListServices(w http.ResponseWriter, r *http.Request) {
	timeRange := parseTimeRange(r)
	services, err := h.Store.ListServices(r.Context(), timeRange)
	if err != nil {
		h.fail(w, "list services", err)
		return
	}
	writeJSON(w, http.StatusOK, withRange(timeRange, "services", emptySlice(services)))
}

// ListOperations returns RED metrics per operation within a service.
func (h *Handlers) ListOperations(w http.ResponseWriter, r *http.Request) {
	timeRange := parseTimeRange(r)
	operations, err := h.Store.ListOperations(r.Context(), r.PathValue("service"), timeRange)
	if err != nil {
		h.fail(w, "list operations", err)
		return
	}
	writeJSON(w, http.StatusOK, withRange(timeRange, "operations", emptySlice(operations)))
}

// TimeSeries returns host-wide throughput and latency over time.
func (h *Handlers) TimeSeries(w http.ResponseWriter, r *http.Request) {
	h.timeSeries(w, r, "")
}

// ServiceTimeSeries returns the same series scoped to one service.
func (h *Handlers) ServiceTimeSeries(w http.ResponseWriter, r *http.Request) {
	h.timeSeries(w, r, r.PathValue("service"))
}

func (h *Handlers) timeSeries(w http.ResponseWriter, r *http.Request, service string) {
	timeRange := parseTimeRange(r)
	step := parseDuration(r.URL.Query().Get("step"), 0)

	points, err := h.Store.TimeSeries(r.Context(), service, timeRange, step)
	if err != nil {
		h.fail(w, "time series", err)
		return
	}
	writeJSON(w, http.StatusOK, withRange(timeRange, "points", emptySlice(points)))
}

// ListMetricNames returns the instruments a service reported, for a picker.
//
// The service defaults to the host, because that is the one source always
// present: an operator opening the metrics view before any JVM is attached
// should still see something.
func (h *Handlers) ListMetricNames(w http.ResponseWriter, r *http.Request) {
	timeRange := parseTimeRange(r)
	service := r.URL.Query().Get("service")
	if service == "" {
		service = hostmetrics.HostService
	}

	names, err := h.Store.ListMetricNames(r.Context(), service, timeRange)
	if err != nil {
		h.fail(w, "list metrics", err)
		return
	}
	body := withRange(timeRange, "metrics", emptySlice(names))
	body["service"] = service
	writeJSON(w, http.StatusOK, body)
}

// QueryMetric returns one instrument's series over a time range.
func (h *Handlers) QueryMetric(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	name := query.Get("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "the name parameter is required")
		return
	}
	service := query.Get("service")
	if service == "" {
		service = hostmetrics.HostService
	}

	timeRange := parseTimeRange(r)
	step := parseDuration(query.Get("step"), 0)

	series, err := h.Store.QueryMetric(r.Context(), service, name, timeRange, step)
	if err != nil {
		h.fail(w, "query metric", err)
		return
	}
	body := withRange(timeRange, "series", emptySlice(series))
	body["service"] = service
	body["name"] = name
	writeJSON(w, http.StatusOK, body)
}

// SearchTraces returns trace summaries matching the query parameters.
func (h *Handlers) SearchTraces(w http.ResponseWriter, r *http.Request) {
	filter := parseTraceFilter(r)
	traces, err := h.Store.SearchTraces(r.Context(), filter)
	if err != nil {
		h.fail(w, "search traces", err)
		return
	}
	writeJSON(w, http.StatusOK, withRange(filter.Range, "traces", emptySlice(traces)))
}

// GetTrace returns every span of one trace.
func (h *Handlers) GetTrace(w http.ResponseWriter, r *http.Request) {
	traceID, err := model.ParseTraceID(r.PathValue("traceID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	trace, err := h.Store.GetTrace(r.Context(), traceID)
	if errors.Is(err, store.ErrTraceNotFound) {
		writeError(w, http.StatusNotFound, "trace not found; it may have passed its retention window")
		return
	}
	if err != nil {
		h.fail(w, "load trace", err)
		return
	}
	writeJSON(w, http.StatusOK, trace)
}

// Dependencies returns the service map edges.
func (h *Handlers) Dependencies(w http.ResponseWriter, r *http.Request) {
	timeRange := parseTimeRange(r)
	deps, err := h.Store.Dependencies(r.Context(), timeRange)
	if err != nil {
		h.fail(w, "service dependencies", err)
		return
	}
	writeJSON(w, http.StatusOK, withRange(timeRange, "dependencies", emptySlice(deps)))
}

// fail logs the underlying error and returns a generic message, so an internal
// SQL error never reaches the browser.
func (h *Handlers) fail(w http.ResponseWriter, what string, err error) {
	h.Log.Error("query failed", "query", what, "error", err)
	writeError(w, http.StatusInternalServerError, "could not "+what)
}

// ---------------------------------------------------------------- helpers

// withRange wraps a result set with the time range it covers, so the UI can
// label a chart without having to remember what it asked for.
func withRange(r store.TimeRange, key string, value any) map[string]any {
	return map[string]any{
		"from": r.From,
		"to":   r.To,
		key:    value,
	}
}

// emptySlice replaces a nil slice with an empty one so the JSON is [] and not
// null, which every client would otherwise have to special-case.
func emptySlice[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// The response is already committed, so this can only be logged.
		slog.Debug("failed to encode response", "error", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
