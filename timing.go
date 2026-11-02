package logx

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

// Timing counts the database work done for one request: how many
// statements and how long they took altogether. It rides on the request context and
// is filled by the Gorm logger, which sees every statement.
type Timing struct {
	queries atomic.Int64
	nanos   atomic.Int64
	mu      sync.Mutex
	stmts   []string // only when LogSQL is on: the statements (placeholders, never values), for finding what a request runs
}

// LogSQL turns on listing each request's statements in its log line (a diagnostic).
// Setup sets it from the environment.
var LogSQL bool

func (t *Timing) note(sql string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.stmts) < 60 {
		t.stmts = append(t.stmts, truncate(sql, 160))
	}
}

type timingKey struct{}

func withTiming(ctx context.Context) (context.Context, *Timing) {
	t := &Timing{}
	return context.WithValue(ctx, timingKey{}, t), t
}

func timingFrom(ctx context.Context) *Timing { t, _ := ctx.Value(timingKey{}).(*Timing); return t }

func (t *Timing) add(d time.Duration) { t.queries.Add(1); t.nanos.Add(int64(d)) }

// Upper bounds, in seconds, of the duration buckets (Prometheus style).
var buckets = [...]float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

type routeStat struct {
	count, errors5xx, queries uint64
	sum, dbSum                float64 // seconds
	le                        [len(buckets)]uint64
}

type routeKey struct{ method, route string }

// Metrics holds per-route counters in memory, cheap enough to keep on all the time:
// one short lock per request and a fixed number of routes (patterns, not paths).
type Metrics struct {
	mu     sync.Mutex
	prefix string
	routes map[routeKey]*routeStat
}

// NewMetrics returns empty metrics whose names start with prefix (for example "app_").
func NewMetrics(prefix string) *Metrics {
	return &Metrics{prefix: prefix, routes: map[routeKey]*routeStat{}}
}

// SetPrefix changes the string put before every metric name.
func (m *Metrics) SetPrefix(prefix string) { m.mu.Lock(); m.prefix = prefix; m.mu.Unlock() }

// DefaultMetrics is fed by Middleware.
var DefaultMetrics = NewMetrics("")

// Observe records one finished request.
func (m *Metrics) Observe(method, route string, status int, d, db time.Duration, queries int64) {
	s := d.Seconds()
	m.mu.Lock()
	defer m.mu.Unlock()
	k := routeKey{method, route}
	r := m.routes[k]
	if r == nil {
		r = &routeStat{}
		m.routes[k] = r
	}
	r.count++
	if status >= 500 {
		r.errors5xx++
	}
	r.sum += s
	r.dbSum += db.Seconds()
	r.queries += uint64(queries)
	for i, b := range buckets {
		if s <= b {
			r.le[i]++
		}
	}
}

// Render renders the Prometheus text format.
func (m *Metrics) Render(w io.Writer) {
	m.mu.Lock()
	keys := make([]routeKey, 0, len(m.routes))
	snap := map[routeKey]routeStat{}
	for k, v := range m.routes {
		keys = append(keys, k)
		snap[k] = *v
	}
	p := m.prefix
	m.mu.Unlock()
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].route != keys[j].route {
			return keys[i].route < keys[j].route
		}
		return keys[i].method < keys[j].method
	})
	fmt.Fprintf(w, "# TYPE %srequest_duration_seconds histogram\n", p)
	for _, k := range keys {
		s := snap[k]
		l := fmt.Sprintf(`method=%q,route=%q`, k.method, k.route)
		for i, b := range buckets {
			fmt.Fprintf(w, "%srequest_duration_seconds_bucket{%s,le=\"%s\"} %d\n", p, l, strconv.FormatFloat(b, 'g', -1, 64), s.le[i])
		}
		fmt.Fprintf(w, "%srequest_duration_seconds_bucket{%s,le=\"+Inf\"} %d\n", p, l, s.count)
		fmt.Fprintf(w, "%srequest_duration_seconds_sum{%s} %g\n", p, l, s.sum)
		fmt.Fprintf(w, "%srequest_duration_seconds_count{%s} %d\n", p, l, s.count)
		fmt.Fprintf(w, "%srequest_errors_total{%s} %d\n", p, l, s.errors5xx)
		fmt.Fprintf(w, "%srequest_db_seconds_total{%s} %g\n", p, l, s.dbSum)
		fmt.Fprintf(w, "%srequest_db_queries_total{%s} %d\n", p, l, s.queries)
	}
}

// MetricsHandler serves the metrics. It is off (404) unless a token is configured, and
// then needs "Authorization: Bearer <token>"; it carries no data, only route patterns and numbers.
func MetricsHandler(token string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if token == "" || c.GetHeader("Authorization") != "Bearer "+token {
			c.Status(http.StatusNotFound)
			return
		}
		c.Header("Content-Type", "text/plain; version=0.0.4")
		DefaultMetrics.Render(c.Writer)
		for _, f := range MetricSources {
			f(c.Writer)
		}
	}
}

// MetricSources are further writers of metrics lines for the same endpoint.
var MetricSources []func(io.Writer)
