package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"regexp"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type middleware func(http.Handler) http.Handler

// chain applies middleware so the first one listed is the outermost.
func chain(h http.Handler, mws ...middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

type ctxKey struct{}

// RequestIDFrom returns the request ID stored in ctx, if any.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

var safeID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if !safeID.MatchString(id) {
			b := make([]byte, 8)
			_, _ = rand.Read(b)
			id = hex.EncodeToString(b)
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)))
	})
}

func recoverer(logger *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if v := recover(); v != nil {
					if v == http.ErrAbortHandler { //nolint:errorlint // sentinel comparison is intended
						panic(v)
					}
					logger.Error("panic", "value", v, "stack", string(debug.Stack()))
					writeProblem(w, http.StatusInternalServerError, "internal", "internal server error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func accessLog(logger *slog.Logger, m *Metrics) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			elapsed := time.Since(start)
			// r.Pattern is set by ServeMux, which keeps metric label
			// cardinality bounded (no raw IDs in labels).
			route := r.Pattern
			if route == "" {
				route = "unmatched"
			}
			if m != nil {
				m.requests.WithLabelValues(route, strconv.Itoa(rec.status)).Inc()
				m.latency.WithLabelValues(route).Observe(elapsed.Seconds())
			}
			if route == "GET /healthz" || route == "GET /metrics" {
				return
			}
			logger.InfoContext(r.Context(), "http request",
				"method", r.Method, "route", route, "status", rec.status,
				"duration_ms", elapsed.Milliseconds(), "request_id", RequestIDFrom(r.Context()))
		})
	}
}

// Metrics holds the Prometheus collectors for the HTTP layer.
type Metrics struct {
	reg      *prometheus.Registry
	requests *prometheus.CounterVec
	latency  *prometheus.HistogramVec
}

// NewMetrics registers HTTP and runtime collectors on a private registry.
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		reg: reg,
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ledgerflow_http_requests_total", Help: "HTTP requests by route and status.",
		}, []string{"route", "status"}),
		latency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "ledgerflow_http_request_duration_seconds", Help: "HTTP request latency.",
			Buckets: prometheus.DefBuckets,
		}, []string{"route"}),
	}
	reg.MustRegister(m.requests, m.latency, collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

// Handler serves the metrics endpoint.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}
