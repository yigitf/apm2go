// Package receiver accepts OpenTelemetry trace data over OTLP and hands it to
// the ingest pipeline.
//
// Both transports the OpenTelemetry SDKs use are served: gRPC, which the Java
// agent uses by default, and HTTP, which browser and PHP SDKs prefer. Speaking
// OTLP rather than a private protocol is what will let phase 2 accept PHP,
// Node.js and Python with no new ingest code.
package receiver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/apm2go/apm2go/internal/ingesttoken"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	metricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	metricpb "go.opentelemetry.io/proto/otlp/metrics/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/apm2go/apm2go/internal/config"
	"github.com/apm2go/apm2go/internal/model"
)

// Consumer receives converted telemetry. The pipeline implements it.
type Consumer interface {
	// Consume takes ownership of the batch. It must not block for long: the
	// caller is an OTLP request that a JVM is waiting on.
	Consume(ctx context.Context, spans []*model.Span) error
	// ConsumeMetrics does the same for metrics.
	ConsumeMetrics(ctx context.Context, metrics []*model.Metric) error
}

// Stats is a snapshot of what the receiver has seen, for the self-monitoring
// endpoint. It deliberately holds no lock: it is copied to callers, and a
// struct that is both copied and lockable is a bug waiting to be written.
type Stats struct {
	RequestsGRPC   int64 `json:"requests_grpc"`
	RequestsHTTP   int64 `json:"requests_http"`
	SpansAccepted  int64 `json:"spans_accepted"`
	SpansMalformed int64 `json:"spans_malformed"`
	SpansRejected  int64 `json:"spans_rejected"`
	LastReceivedAt int64 `json:"last_received_at"`
	// MetricsAccepted and MetricsMalformed mirror the span counters. A metric
	// counted as malformed is one apm2go received but does not store, such as
	// an exponential histogram.
	MetricsAccepted  int64 `json:"metrics_accepted"`
	MetricsMalformed int64 `json:"metrics_malformed"`
	// Unauthenticated counts exports turned away for a missing or unrecognised
	// token. On a host with no containers this being non-zero means something
	// apm2go did not instrument is exporting to it.
	Unauthenticated int64 `json:"unauthenticated"`
	// ListenAddresses is every address ingest is currently reachable on, which
	// grows as container networks are discovered.
	ListenAddresses []string `json:"listen_addresses,omitempty"`
}

// counters is the mutable state behind Stats, guarded by its own lock and never
// copied.
type counters struct {
	mu               sync.Mutex
	requestsGRPC     int64
	requestsHTTP     int64
	spansAccepted    int64
	spansMalformed   int64
	spansRejected    int64
	lastReceivedAt   int64
	metricsAccepted  int64
	metricsMalformed int64
}

func (c *counters) snapshot() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Stats{
		RequestsGRPC:     c.requestsGRPC,
		RequestsHTTP:     c.requestsHTTP,
		SpansAccepted:    c.spansAccepted,
		SpansMalformed:   c.spansMalformed,
		SpansRejected:    c.spansRejected,
		LastReceivedAt:   c.lastReceivedAt,
		MetricsAccepted:  c.metricsAccepted,
		MetricsMalformed: c.metricsMalformed,
	}
}

// Receiver serves the OTLP endpoints.
type Receiver struct {
	cfg      config.ReceiverConfig
	consumer Consumer
	log      *slog.Logger
	stats    counters
	auth     *authenticator

	// Serving state, set up by Run. Gateways discovered later are bound onto
	// these, so ingest reaches containers found after start-up.
	grpcServer  *grpc.Server
	httpServer  *http.Server
	grpcBinder  *binder
	httpBinder  *binder
	serveGroup  sync.WaitGroup
	serving     chan struct{}
	servingOnce sync.Once
}

// New returns a Receiver that forwards spans to consumer.
func New(cfg config.ReceiverConfig, consumer Consumer, tokens *ingesttoken.Registry, log *slog.Logger) *Receiver {
	return &Receiver{
		cfg:      cfg,
		consumer: consumer,
		log:      log,
		auth:     newAuthenticator(tokens, cfg.RequireToken),
		serving:  make(chan struct{}),
	}
}

// Name identifies this component in logs.
func (r *Receiver) Name() string { return "otlp-receiver" }

// Stats returns a snapshot of ingest counters.
func (r *Receiver) Stats() Stats {
	stats := r.stats.snapshot()
	stats.Unauthenticated = r.auth.rejectedCount()
	stats.ListenAddresses = r.ListenAddresses()
	return stats
}

// Run serves both transports until ctx is cancelled. If either listener fails
// to bind, neither starts: a half-listening collector silently drops the data
// sent to the other port.
func (r *Receiver) Run(ctx context.Context) error {
	grpcAddr, err := resolveListenAddr(r.cfg.ContainerBind, r.cfg.GRPCAddr)
	if err != nil {
		return err
	}
	httpAddr, err := resolveListenAddr(r.cfg.ContainerBind, r.cfg.HTTPAddr)
	if err != nil {
		return err
	}

	grpcBinder, grpcListener, err := newBinder(r.cfg.ContainerBind, grpcAddr,
		func(addr string) { r.log.Info("OTLP/gRPC also listening", "addr", addr) })
	if err != nil {
		return fmt.Errorf("OTLP/gRPC: %w", err)
	}
	httpBinder, httpListener, err := newBinder(r.cfg.ContainerBind, httpAddr,
		func(addr string) { r.log.Info("OTLP/HTTP also listening", "addr", addr) })
	if err != nil {
		grpcBinder.close()
		return fmt.Errorf("OTLP/HTTP: %w", err)
	}

	r.grpcBinder, r.httpBinder = grpcBinder, httpBinder

	r.grpcServer = grpc.NewServer(
		grpc.MaxRecvMsgSize(r.cfg.MaxRecvMsgBytes),
		grpc.UnaryInterceptor(r.auth.unaryInterceptor()),
	)
	collectorpb.RegisterTraceServiceServer(r.grpcServer, &traceService{r: r})
	metricspb.RegisterMetricsServiceServer(r.grpcServer, &metricsService{r: r})

	r.httpServer = &http.Server{
		Handler:           r.httpHandler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	r.log.Info("OTLP receiver listening",
		"grpc", grpcAddr, "http", httpAddr,
		"container_bind", r.cfg.ContainerBind, "require_token", r.cfg.RequireToken)

	errCh := make(chan error, 2)
	r.serveGRPC(grpcListener, errCh)
	r.serveHTTP(httpListener, errCh)

	// Signals that later listeners may now be added.
	r.servingOnce.Do(func() { close(r.serving) })

	select {
	case <-ctx.Done():
	case err := <-errCh:
		r.grpcServer.Stop()
		_ = r.httpServer.Close()
		grpcBinder.close()
		httpBinder.close()
		return err
	}

	// Let in-flight exports finish rather than dropping a JVM's last batch.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		r.grpcServer.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
	case <-shutdownCtx.Done():
		r.grpcServer.Stop()
	}
	_ = r.httpServer.Shutdown(shutdownCtx)

	grpcBinder.close()
	httpBinder.close()
	r.serveGroup.Wait()

	return ctx.Err()
}

// serveGRPC starts serving one gRPC listener.
func (r *Receiver) serveGRPC(listener net.Listener, errCh chan<- error) {
	r.serveGroup.Add(1)
	go func() {
		defer r.serveGroup.Done()
		if err := r.grpcServer.Serve(listener); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			select {
			case errCh <- fmt.Errorf("OTLP/gRPC server: %w", err):
			default:
			}
		}
	}()
}

// serveHTTP starts serving one HTTP listener.
func (r *Receiver) serveHTTP(listener net.Listener, errCh chan<- error) {
	r.serveGroup.Add(1)
	go func() {
		defer r.serveGroup.Done()
		if err := r.httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			select {
			case errCh <- fmt.Errorf("OTLP/HTTP server: %w", err):
			default:
			}
		}
	}()
}

// ListenOnGateway extends ingest to a container network's gateway, so
// applications on that network can export to apm2go.
//
// Discovery calls this as containers are found. It is safe to call repeatedly
// with the same address, and does nothing unless container_bind is "auto" —
// "all" already covers every address and "off" declines by design.
func (r *Receiver) ListenOnGateway(host string) {
	if r.grpcBinder == nil || r.httpBinder == nil {
		return
	}
	// Adding a listener before the servers exist would race their construction.
	select {
	case <-r.serving:
	default:
		return
	}

	errCh := make(chan error, 2)
	if listener, err := r.grpcBinder.listenOn(host); err != nil {
		r.log.Warn("could not extend OTLP/gRPC to a container gateway", "gateway", host, "error", err)
	} else if listener != nil {
		r.serveGRPC(listener, errCh)
	}
	if listener, err := r.httpBinder.listenOn(host); err != nil {
		r.log.Warn("could not extend OTLP/HTTP to a container gateway", "gateway", host, "error", err)
	} else if listener != nil {
		r.serveHTTP(listener, errCh)
	}
}

// ListenAddresses reports every address ingest is currently reachable on.
func (r *Receiver) ListenAddresses() []string {
	if r.grpcBinder == nil {
		return nil
	}
	return r.grpcBinder.addresses()
}

// The OTLP trace and metrics services both name their method Export, so a
// single type cannot implement both. Each gets a thin adapter that carries the
// right signature and delegates; all the actual work stays on Receiver, where
// the counters and the consumer live.

// traceService adapts Receiver to the OTLP trace service.
type traceService struct {
	collectorpb.UnimplementedTraceServiceServer
	r *Receiver
}

// metricsService adapts Receiver to the OTLP metrics service.
type metricsService struct {
	metricspb.UnimplementedMetricsServiceServer
	r *Receiver
}

// Export implements the OTLP gRPC trace service.
func (s *traceService) Export(ctx context.Context, req *collectorpb.ExportTraceServiceRequest) (*collectorpb.ExportTraceServiceResponse, error) {
	return s.r.exportTraces(ctx, req)
}

// Export implements the OTLP gRPC metrics service.
func (s *metricsService) Export(ctx context.Context, req *metricspb.ExportMetricsServiceRequest) (*metricspb.ExportMetricsServiceResponse, error) {
	return s.r.exportMetrics(ctx, req)
}

// exportTraces handles a trace export.
func (r *Receiver) exportTraces(ctx context.Context, req *collectorpb.ExportTraceServiceRequest) (*collectorpb.ExportTraceServiceResponse, error) {
	r.countRequest(false)

	rejected, err := r.ingest(ctx, req.GetResourceSpans())
	if err != nil {
		// Unavailable tells the SDK to retry, which is the right answer when we
		// are shedding load rather than rejecting the data itself.
		return nil, status.Error(codes.Unavailable, err.Error())
	}

	resp := &collectorpb.ExportTraceServiceResponse{}
	if rejected > 0 {
		// The partial success field is how OTLP reports "kept some, dropped
		// some" without failing the whole export.
		resp.PartialSuccess = &collectorpb.ExportTracePartialSuccess{
			RejectedSpans: rejected,
			ErrorMessage:  "some spans were malformed and could not be stored",
		}
	}
	return resp, nil
}

// exportMetrics handles a metrics export.
func (r *Receiver) exportMetrics(ctx context.Context, req *metricspb.ExportMetricsServiceRequest) (*metricspb.ExportMetricsServiceResponse, error) {
	r.countRequest(false)

	rejected, err := r.ingestMetrics(ctx, req.GetResourceMetrics())
	if err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}

	resp := &metricspb.ExportMetricsServiceResponse{}
	if rejected > 0 {
		resp.PartialSuccess = &metricspb.ExportMetricsPartialSuccess{
			RejectedDataPoints: rejected,
			ErrorMessage:       "some data points could not be stored",
		}
	}
	return resp, nil
}

// ingestMetrics converts and forwards a metric batch, returning how many points
// were dropped.
func (r *Receiver) ingestMetrics(ctx context.Context, resourceMetrics []*metricpb.ResourceMetrics) (int64, error) {
	metrics, skipped := ConvertMetrics(resourceMetrics)

	r.stats.mu.Lock()
	r.stats.metricsAccepted += int64(len(metrics))
	r.stats.metricsMalformed += int64(skipped)
	r.stats.lastReceivedAt = time.Now().Unix()
	r.stats.mu.Unlock()

	if len(metrics) == 0 {
		return int64(skipped), nil
	}
	if err := r.consumer.ConsumeMetrics(ctx, metrics); err != nil {
		return int64(skipped), err
	}
	return int64(skipped), nil
}

// ingest converts and forwards a batch, returning how many spans were dropped.
func (r *Receiver) ingest(ctx context.Context, resourceSpans []*tracepbResourceSpans) (int64, error) {
	spans, skipped := Convert(resourceSpans)

	r.stats.mu.Lock()
	r.stats.spansAccepted += int64(len(spans))
	r.stats.spansMalformed += int64(skipped)
	r.stats.lastReceivedAt = time.Now().Unix()
	r.stats.mu.Unlock()

	if len(spans) == 0 {
		return int64(skipped), nil
	}

	if err := r.consumer.Consume(ctx, spans); err != nil {
		r.stats.mu.Lock()
		r.stats.spansRejected += int64(len(spans))
		r.stats.mu.Unlock()
		return int64(skipped), err
	}
	return int64(skipped), nil
}

func (r *Receiver) countRequest(isHTTP bool) {
	r.stats.mu.Lock()
	defer r.stats.mu.Unlock()
	if isHTTP {
		r.stats.requestsHTTP++
	} else {
		r.stats.requestsGRPC++
	}
}

// tracepbResourceSpans aliases the OTLP type so the ingest signature stays
// readable; the protobuf package name carries no meaning at the call site.
type tracepbResourceSpans = tracepb.ResourceSpans
