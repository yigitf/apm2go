package receiver

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/apm2go/apm2go/internal/ingesttoken"
)

// Once the receiver listens on a container network's gateway, every container
// on that bridge can reach it. The token apm2go injected into each process is
// what distinguishes telemetry it produced from telemetry anyone else can send,
// and an APM that accepts fabricated spans is worse than one missing data:
// the fabrication is indistinguishable from a measurement.

// authenticator checks ingest credentials, and counts what it turns away.
type authenticator struct {
	tokens   *ingesttoken.Registry
	required bool
	rejected atomic.Int64
}

func newAuthenticator(tokens *ingesttoken.Registry, required bool) *authenticator {
	return &authenticator{tokens: tokens, required: required}
}

// permits reports whether a request carrying this token may write telemetry.
func (a *authenticator) permits(token string) bool {
	if a == nil || !a.required || a.tokens == nil {
		return true
	}
	if a.tokens.Accepts(token) {
		return true
	}
	a.rejected.Add(1)
	return false
}

// rejectedCount reports how many exports were turned away, for self-monitoring.
// A non-zero value on a host with no containers usually means something on the
// network is exporting to apm2go that apm2go did not instrument.
func (a *authenticator) rejectedCount() int64 {
	if a == nil {
		return 0
	}
	return a.rejected.Load()
}

// unaryInterceptor enforces the token on the gRPC transport.
func (a *authenticator) unaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if a == nil || !a.required {
			return handler(ctx, req)
		}
		if a.permits(tokenFromContext(ctx)) {
			return handler(ctx, req)
		}
		// Unauthenticated rather than PermissionDenied: the SDK should not
		// retry, because retrying without a token will fail identically.
		return nil, status.Error(codes.Unauthenticated,
			"missing or unrecognised ingest token; this telemetry was not produced by an apm2go-instrumented process")
	}
}

// tokenFromContext reads the token out of gRPC metadata. Metadata keys are
// lowercased by the transport, which is why the header constant is lowercase.
func tokenFromContext(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	values := md.Get(ingesttoken.Header)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

// tokenFromRequest reads the token off an HTTP request.
func tokenFromRequest(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get(ingesttoken.Header))
}
