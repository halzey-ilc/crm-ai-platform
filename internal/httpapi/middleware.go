package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"crmai/internal/auth"
)

type ctxKey int

const (
	keyCorrelation ctxKey = iota
	keyPrincipal
)

const HeaderCorrelation = "X-Correlation-ID"

var safeID = regexp.MustCompile(`^[A-Za-z0-9._-]{8,128}$`)

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// CorrelationID returns the request's correlation id.
func CorrelationID(ctx context.Context) string {
	s, _ := ctx.Value(keyCorrelation).(string)
	return s
}

func principalFrom(ctx context.Context) (auth.Principal, bool) {
	p, ok := ctx.Value(keyPrincipal).(auth.Principal)
	return p, ok
}

// withCorrelation accepts an inbound correlation id only if it is log-safe,
// otherwise mints one, and echoes it in the response.
func withCorrelation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(HeaderCorrelation)
		if !safeID.MatchString(id) {
			id = newID()
		}
		w.Header().Set(HeaderCorrelation, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), keyCorrelation, id)))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// withAccessLog logs method, route pattern, status and latency. Never bodies.
func withAccessLog(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rec, r)
		attrs := []any{"method", r.Method, "path", r.URL.Path, "status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(), "correlation_id", CorrelationID(r.Context())}
		log.Info("http request", attrs...)
	})
}

// withRecover turns panics into a generic 500 without leaking details.
func withRecover(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				log.Error("panic in handler", "panic", v, "correlation_id", CorrelationID(r.Context()))
				writeError(w, r, http.StatusInternalServerError, "internal_error", "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// withLimits bounds request time and body size. File uploads (recordings,
// knowledge files) get their own, larger limit and at least two minutes to arrive.
func withLimits(timeout time.Duration, maxBody, maxUpload int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit, budget := maxBody, timeout
		if maxUpload > 0 && (strings.HasPrefix(r.URL.Path, "/v1/audio/") || strings.HasPrefix(r.URL.Path, "/v1/knowledge/files/")) {
			limit, budget = maxUpload, max(timeout, 2*time.Minute)
		}
		ctx, cancel := context.WithTimeout(r.Context(), budget)
		defer cancel()
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// withAuth verifies the bearer token and an optional X-Tenant header, which
// must match the token's tenant if present.
func withAuth(log *slog.Logger, v *auth.Verifier, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		raw, ok := strings.CutPrefix(h, "Bearer ")
		if !ok || raw == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="crm-ai-platform"`)
			writeError(w, r, http.StatusUnauthorized, "unauthenticated", "missing bearer token")
			return
		}
		p, err := v.Verify(raw)
		if err != nil {
			log.Info("token rejected", "reason", err.Error(), "correlation_id", CorrelationID(r.Context()))
			w.Header().Set("WWW-Authenticate", `Bearer realm="crm-ai-platform", error="invalid_token"`)
			writeError(w, r, http.StatusUnauthorized, "unauthenticated", "invalid or expired token")
			return
		}
		if t := r.Header.Get("X-Tenant"); t != "" && t != p.Tenant {
			writeError(w, r, http.StatusForbidden, "tenant_mismatch", "X-Tenant does not match the token's tenant")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), keyPrincipal, p)))
	})
}

// rateLimiter is a per-(tenant, subject) token bucket. In-process only: with
// several gateway replicas the effective limit is per replica.
type rateLimiter struct {
	mu      sync.Mutex
	rps     rate.Limit
	burst   int
	buckets map[string]*bucket
}

type bucket struct {
	lim  *rate.Limiter
	seen time.Time
}

func newRateLimiter(rps float64, burst int) *rateLimiter {
	return &rateLimiter{rps: rate.Limit(rps), burst: burst, buckets: map[string]*bucket{}}
}

func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if len(l.buckets) > 10000 { // drop idle buckets to bound memory
		for k, b := range l.buckets {
			if now.Sub(b.seen) > 10*time.Minute {
				delete(l.buckets, k)
			}
		}
	}
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{lim: rate.NewLimiter(l.rps, l.burst)}
		l.buckets[key] = b
	}
	b.seen = now
	return b.lim.Allow()
}

func (l *rateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := principalFrom(r.Context())
		if !l.allow(p.Tenant + "\x00" + p.Subject) {
			w.Header().Set("Retry-After", "1")
			writeError(w, r, http.StatusTooManyRequests, "rate_limited", "too many requests")
			return
		}
		next.ServeHTTP(w, r)
	})
}
