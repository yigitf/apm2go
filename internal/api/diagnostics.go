package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/yigitf/apm2go/internal/jvmdiag"
	"github.com/yigitf/apm2go/internal/store"
)

// diagnosticTimeout bounds one diagnostic command. It is longer than an attach
// because the target may be at a safepoint doing the work, and shorter than any
// reasonable browser timeout so the UI gets an answer rather than a hung tab.
const diagnosticTimeout = 90 * time.Second

// storedHistogramClasses is how many classes of a heap histogram are kept and
// returned. The tail is thousands of classes holding a few hundred bytes each:
// it would dominate the database and tell an operator nothing.
const storedHistogramClasses = 250

// diffLimit bounds each side of a histogram comparison.
const diffLimit = 50

// CollectDiagnostic runs a diagnostic command against a JVM and stores the
// result.
//
// This is a POST because it is not free: the target stops at a safepoint for as
// long as the command takes. Nothing in apm2go issues these on a timer, and
// this handler is the only path that issues them at all.
func (h *Handlers) CollectDiagnostic(w http.ResponseWriter, r *http.Request) {
	if h.Diagnostics == nil {
		writeError(w, http.StatusNotImplemented,
			"this apm2go instance does not manage JVMs, so it cannot run diagnostics")
		return
	}
	pid, ok := h.requirePID(w, r)
	if !ok {
		return
	}

	kind := jvmdiag.Kind(r.PathValue("kind"))
	if !kind.Valid() {
		writeError(w, http.StatusBadRequest, "unknown diagnostic "+strconv.Quote(string(kind)))
		return
	}

	entry, err := h.Inventory.Get(pid)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	ctx, cancel := contextWithTimeout(r, diagnosticTimeout)
	defer cancel()

	dump, err := h.Diagnostics.Collect(ctx, entry.JVM, kind)
	if err != nil {
		// A JVM that refuses the command is not a server fault: it may have
		// exited, or be a build that does not offer this diagnostic.
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	// The full histogram goes neither to the database nor down the wire.
	if dump.Histogram != nil {
		dump.Histogram = dump.Histogram.Top(storedHistogramClasses)
	}

	id, err := h.storeDiagnostic(r, dump, entry.JVM.StartTime)
	if err != nil {
		// The dump was collected — the target already paid for it — so losing
		// the ability to store it must not also lose the answer.
		h.Log.Error("could not store diagnostic",
			"pid", pid, "kind", kind, "error", err)
	}

	writeJSON(w, http.StatusOK, diagnosticResponse(id, dump))
}

// storeDiagnostic persists a dump and returns the id it was filed under. An
// empty id means it was not stored, which is the case in agent mode.
func (h *Handlers) storeDiagnostic(r *http.Request, dump *jvmdiag.Dump, startTime time.Time) (string, error) {
	if h.Store == nil {
		return "", nil
	}

	summary, err := json.Marshal(summaryOf(dump))
	if err != nil {
		return "", err
	}
	headline, err := json.Marshal(dump.Headline())
	if err != nil {
		return "", err
	}

	id := newDiagnosticID()
	record := &store.Diagnostic{
		ID:         id,
		TS:         dump.CapturedAt,
		Kind:       string(dump.Kind),
		PID:        dump.PID,
		StartTime:  startTime,
		Service:    dump.Service,
		DurationMS: dump.DurationMS,
		SizeBytes:  int64(len(dump.Raw)),
		Headline:   headline,
		Summary:    summary,
		Raw:        dump.Raw,
	}

	// Storing must not be bounded by the request: the browser may already have
	// given up, and the dump is the expensive part that cannot be recollected.
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()

	if err := h.Store.WriteDiagnostic(ctx, record); err != nil {
		return "", err
	}
	return id, nil
}

// summaryOf is the parsed part of a dump, without the raw text.
func summaryOf(dump *jvmdiag.Dump) map[string]any {
	out := map[string]any{}
	if dump.Threads != nil {
		out["threads"] = dump.Threads
	}
	if dump.Histogram != nil {
		out["histogram"] = dump.Histogram
	}
	if dump.Heap != nil {
		out["heap"] = dump.Heap
	}
	return out
}

// diagnosticResponse is what a collection returns.
//
// The raw output is deliberately left out: a thread dump runs to megabytes and
// the UI renders the parsed form. The raw endpoint serves it when asked.
func diagnosticResponse(id string, dump *jvmdiag.Dump) map[string]any {
	out := map[string]any{
		"id":          id,
		"kind":        dump.Kind,
		"pid":         dump.PID,
		"service":     dump.Service,
		"captured_at": dump.CapturedAt,
		"duration_ms": dump.DurationMS,
		"size_bytes":  len(dump.Raw),
		"summary":     summaryOf(dump),
	}
	if id == "" {
		out["stored"] = false
		out["note"] = "this dump was not stored, so it cannot be compared against a later one"
	} else {
		out["stored"] = true
	}
	// VM.flags has no parser; its whole value is the text.
	if dump.Kind == jvmdiag.KindVMFlags {
		out["text"] = dump.Raw
	}
	return out
}

// ListDiagnostics returns the dumps stored for one JVM, newest first.
func (h *Handlers) ListDiagnostics(w http.ResponseWriter, r *http.Request) {
	pid, ok := parsePID(w, r)
	if !ok {
		return
	}
	if h.Store == nil {
		writeJSON(w, http.StatusOK, map[string]any{"diagnostics": []store.Diagnostic{}})
		return
	}

	filter := store.DiagnosticFilter{PID: pid, Kind: r.URL.Query().Get("kind")}
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
		filter.Limit = n
	}

	list, err := h.Store.ListDiagnostics(r.Context(), filter)
	if err != nil {
		h.fail(w, "list diagnostics", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"diagnostics": emptySlice(list),
		"available":   jvmdiag.Kinds(),
		// The one diagnostic apm2go will not run, and why. The UI shows the
		// command so an operator can run it deliberately, in a window where
		// stopping the application for minutes is acceptable.
		"heap_dump": map[string]string{
			"command": jvmdiag.HeapDumpCommand,
			"note": "apm2go does not take heap dumps. GC.heap_dump writes the whole heap to " +
				"disk and holds a safepoint until it finishes, which on a live service is an " +
				"outage rather than an observation. Run it yourself during a maintenance window.",
		},
	})
}

// GetDiagnostic returns one stored dump, parsed.
func (h *Handlers) GetDiagnostic(w http.ResponseWriter, r *http.Request) {
	dump, ok := h.requireDiagnostic(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	dump.Raw = ""
	writeJSON(w, http.StatusOK, dump)
}

// GetDiagnosticRaw serves a stored dump's verbatim output as plain text, which
// is what an operator pastes into a bug report or feeds to another tool.
func (h *Handlers) GetDiagnosticRaw(w http.ResponseWriter, r *http.Request) {
	dump, ok := h.requireDiagnostic(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition",
		"attachment; filename="+strconv.Quote(dump.Kind+"-"+strconv.Itoa(dump.PID)+".txt"))
	_, _ = w.Write([]byte(dump.Raw))
}

// CompareDiagnostics diffs two stored class histograms.
//
// Comparing histograms is how a leak is found: a single dump shows what is big,
// two show what is growing. Only histograms of the same process can be
// compared — the same class in two different services says nothing.
func (h *Handlers) CompareDiagnostics(w http.ResponseWriter, r *http.Request) {
	fromID := r.URL.Query().Get("from")
	toID := r.URL.Query().Get("to")
	if fromID == "" || toID == "" {
		writeError(w, http.StatusBadRequest, "both 'from' and 'to' diagnostic ids are required")
		return
	}

	earlier, ok := h.requireDiagnostic(w, r, fromID)
	if !ok {
		return
	}
	later, ok := h.requireDiagnostic(w, r, toID)
	if !ok {
		return
	}

	if earlier.Kind != string(jvmdiag.KindClassHistogram) || later.Kind != string(jvmdiag.KindClassHistogram) {
		writeError(w, http.StatusBadRequest, "only class histograms can be compared")
		return
	}
	// A pid alone is not identity: the same number can name a different process
	// after a restart, and diffing across that boundary invents a leak.
	if earlier.PID != later.PID || !earlier.StartTime.Equal(later.StartTime) {
		writeError(w, http.StatusBadRequest,
			"these dumps come from different processes; a histogram can only be compared against one from the same JVM")
		return
	}
	if later.TS.Before(earlier.TS) {
		earlier, later = later, earlier
	}

	first, err := histogramOf(earlier)
	if err != nil {
		h.fail(w, "read earlier histogram", err)
		return
	}
	second, err := histogramOf(later)
	if err != nil {
		h.fail(w, "read later histogram", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"from":    earlier.TS,
		"to":      later.TS,
		"pid":     earlier.PID,
		"service": earlier.Service,
		// Rounded: the gap between two dumps is meaningful to the second, and
		// microsecond precision only makes the sentence around it harder to read.
		"elapsed":   later.TS.Sub(earlier.TS).Round(time.Second).String(),
		"diff":      jvmdiag.Diff(first, second, diffLimit),
		"truncated": first.ClassCount > len(first.Classes) || second.ClassCount > len(second.Classes),
	})
}

// histogramOf pulls the parsed histogram back out of a stored summary.
func histogramOf(d *store.Diagnostic) (*jvmdiag.ClassHistogram, error) {
	var envelope struct {
		Histogram *jvmdiag.ClassHistogram `json:"histogram"`
	}
	if err := json.Unmarshal(d.Summary, &envelope); err != nil {
		return nil, err
	}
	if envelope.Histogram == nil {
		return nil, errors.New("stored dump has no parsed histogram")
	}
	return envelope.Histogram, nil
}

// requireDiagnostic loads a stored dump or writes the error response.
func (h *Handlers) requireDiagnostic(w http.ResponseWriter, r *http.Request, id string) (*store.Diagnostic, bool) {
	if h.Store == nil {
		writeError(w, http.StatusNotImplemented, "this apm2go instance does not store diagnostics")
		return nil, false
	}
	if id == "" {
		writeError(w, http.StatusBadRequest, "diagnostic id is required")
		return nil, false
	}

	dump, err := h.Store.GetDiagnostic(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrDiagnosticNotFound):
		// Aged out of retention is an ordinary outcome, not a failure.
		writeError(w, http.StatusNotFound, "no diagnostic with id "+strconv.Quote(id))
		return nil, false
	case err != nil:
		h.fail(w, "read diagnostic", err)
		return nil, false
	}
	return dump, true
}

// newDiagnosticID mints an opaque id for a stored dump. It carries no meaning:
// a timestamp-derived id would collide the moment two dumps were taken in the
// same instant, which is exactly what a comparison workflow does.
func newDiagnosticID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand does not fail in practice, and a dump that cannot be
		// filed is still worth returning, so fall back to a time-based id.
		return "diag-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(buf[:])
}
