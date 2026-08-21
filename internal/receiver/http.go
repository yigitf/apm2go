package receiver

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	metricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
)

// OTLP over HTTP carries the same messages as the gRPC service, encoded either
// as protobuf or as JSON. Both are accepted: SDKs differ in their default, and
// a mismatch would otherwise present as silent data loss.
const (
	contentTypeProtobuf = "application/x-protobuf"
	contentTypeJSON     = "application/json"
)

// The OTLP/HTTP endpoints every SDK posts to.
const (
	tracesPath  = "/v1/traces"
	metricsPath = "/v1/metrics"
)

// httpHandler builds the OTLP/HTTP mux.
func (r *Receiver) httpHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(tracesPath, r.handleTraces)
	mux.HandleFunc(metricsPath, r.handleMetrics)
	// A plain health endpoint makes the receiver easy to probe from a shell or
	// a container health check, without involving the API port.
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})
	return mux
}

// handleTraces accepts an OTLP/HTTP trace export.
func (r *Receiver) handleTraces(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "OTLP requires POST", http.StatusMethodNotAllowed)
		return
	}
	r.countRequest(true)

	if !r.auth.permits(tokenFromRequest(req)) {
		http.Error(w,
			"missing or unrecognised ingest token; this telemetry was not produced by an apm2go-instrumented process",
			http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(io.LimitReader(req.Body, int64(r.cfg.MaxRecvMsgBytes)))
	if err != nil {
		http.Error(w, "could not read request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	contentType := normalizeContentType(req.Header.Get("Content-Type"))

	var exportReq collectorpb.ExportTraceServiceRequest
	switch contentType {
	case contentTypeJSON:
		// DiscardUnknown keeps a newer SDK from failing against an older
		// collector over a field apm2go does not read anyway.
		err = protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(body, &exportReq)
	default:
		err = proto.Unmarshal(body, &exportReq)
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("could not decode %s payload: %v", contentType, err), http.StatusBadRequest)
		return
	}

	rejected, err := r.ingest(req.Context(), exportReq.GetResourceSpans())
	if err != nil {
		// 503 asks the SDK to retry, matching the gRPC Unavailable above.
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	resp := &collectorpb.ExportTraceServiceResponse{}
	if rejected > 0 {
		resp.PartialSuccess = &collectorpb.ExportTracePartialSuccess{
			RejectedSpans: rejected,
			ErrorMessage:  "some spans were malformed and could not be stored",
		}
	}
	writeResponse(w, contentType, resp)
}

// handleMetrics accepts an OTLP/HTTP metrics export. It mirrors handleTraces;
// the two differ only in the message type they decode and where they forward.
func (r *Receiver) handleMetrics(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "OTLP requires POST", http.StatusMethodNotAllowed)
		return
	}
	r.countRequest(true)

	if !r.auth.permits(tokenFromRequest(req)) {
		http.Error(w,
			"missing or unrecognised ingest token; this telemetry was not produced by an apm2go-instrumented process",
			http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(io.LimitReader(req.Body, int64(r.cfg.MaxRecvMsgBytes)))
	if err != nil {
		http.Error(w, "could not read request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	contentType := normalizeContentType(req.Header.Get("Content-Type"))

	var exportReq metricspb.ExportMetricsServiceRequest
	switch contentType {
	case contentTypeJSON:
		err = protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(body, &exportReq)
	default:
		err = proto.Unmarshal(body, &exportReq)
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("could not decode %s payload: %v", contentType, err), http.StatusBadRequest)
		return
	}

	rejected, err := r.ingestMetrics(req.Context(), exportReq.GetResourceMetrics())
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	resp := &metricspb.ExportMetricsServiceResponse{}
	if rejected > 0 {
		resp.PartialSuccess = &metricspb.ExportMetricsPartialSuccess{
			RejectedDataPoints: rejected,
			ErrorMessage:       "some data points could not be stored",
		}
	}
	writeProtoResponse(w, contentType, resp)
}

// writeResponse replies in the same encoding the client used, as OTLP requires.
func writeResponse(w http.ResponseWriter, contentType string, resp *collectorpb.ExportTraceServiceResponse) {
	writeProtoResponse(w, contentType, resp)
}

// writeProtoResponse encodes any OTLP response in the encoding the client used.
func writeProtoResponse(w http.ResponseWriter, contentType string, resp proto.Message) {
	var (
		payload []byte
		err     error
	)
	if contentType == contentTypeJSON {
		payload, err = protojson.Marshal(resp)
	} else {
		payload, err = proto.Marshal(resp)
	}
	if err != nil {
		http.Error(w, "could not encode response: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}

// normalizeContentType strips parameters such as "; charset=utf-8" and defaults
// to protobuf, which is what an SDK sending no header means.
func normalizeContentType(raw string) string {
	base, _, _ := strings.Cut(raw, ";")
	switch strings.TrimSpace(strings.ToLower(base)) {
	case contentTypeJSON:
		return contentTypeJSON
	default:
		return contentTypeProtobuf
	}
}
