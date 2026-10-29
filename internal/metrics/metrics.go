// Package metrics is a small Prometheus exporter (text exposition format
// 0.0.4): counters, histograms and gauges with fixed label names. It exists to
// avoid a large client dependency for a handful of series.
//
// Labels must be low-cardinality and carry no tenant or personal data: job
// types, tasks, providers, outcomes, routes with ids replaced.
package metrics

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Registry holds metric families and renders them.
type Registry struct {
	mu       sync.Mutex
	families []family
}

type family interface {
	write(w io.Writer)
	name() string
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{} }

// Default is the process-wide registry.
var Default = NewRegistry()

func (r *Registry) add(f family) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.families {
		if existing.name() == f.name() {
			panic("metrics: duplicate metric " + f.name())
		}
	}
	r.families = append(r.families, f)
}

// Handler serves the registry in the Prometheus text format.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		r.Write(w)
	})
}

// Write renders every family, sorted by name.
func (r *Registry) Write(w io.Writer) {
	r.mu.Lock()
	fams := append([]family(nil), r.families...)
	r.mu.Unlock()
	sort.Slice(fams, func(i, j int) bool { return fams[i].name() < fams[j].name() })
	for _, f := range fams {
		f.write(w)
	}
}

type series struct {
	labels []string
	value  float64
}

type base struct {
	metric, help, kind string
	labelNames         []string
	mu                 sync.Mutex
}

func (b *base) name() string { return b.metric }

func (b *base) key(values []string) string {
	if len(values) != len(b.labelNames) {
		panic(fmt.Sprintf("metrics: %s takes %d labels, got %d", b.metric, len(b.labelNames), len(values)))
	}
	return strings.Join(values, "\xff")
}

func (b *base) header(w io.Writer) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", b.metric, b.help, b.metric, b.kind)
}

func labelText(names, values []string, extra ...string) string {
	parts := make([]string, 0, len(names)+1)
	for i, n := range names {
		parts = append(parts, n+`="`+escape(values[i])+`"`)
	}
	parts = append(parts, extra...)
	if len(parts) == 0 {
		return ""
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func escape(v string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(v)
}

func number(v float64) string {
	if math.IsInf(v, 1) {
		return "+Inf"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// --- counter -------------------------------------------------------------------

// Counter is a monotonically increasing value per label set.
type Counter struct {
	base
	values map[string]*series
}

// NewCounter registers a counter.
func (r *Registry) NewCounter(name, help string, labels ...string) *Counter {
	c := &Counter{base: base{metric: name, help: help, kind: "counter", labelNames: labels}, values: map[string]*series{}}
	r.add(c)
	return c
}

// Add increases the counter for the label values by v (v >= 0).
func (c *Counter) Add(v float64, labels ...string) {
	if v < 0 {
		return
	}
	k := c.key(labels)
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.values[k]
	if !ok {
		s = &series{labels: append([]string(nil), labels...)}
		c.values[k] = s
	}
	s.value += v
}

// Inc adds one.
func (c *Counter) Inc(labels ...string) { c.Add(1, labels...) }

// Value returns the current value (for tests).
func (c *Counter) Value(labels ...string) float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.values[c.key(labels)]; ok {
		return s.value
	}
	return 0
}

func (c *Counter) write(w io.Writer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.header(w)
	for _, k := range sortedKeys(c.values) {
		s := c.values[k]
		fmt.Fprintf(w, "%s%s %s\n", c.metric, labelText(c.labelNames, s.labels), number(s.value))
	}
}

// --- gauge -----------------------------------------------------------------------

// Gauge is a value that can go up and down, set per label set.
type Gauge struct {
	base
	values map[string]*series
}

// NewGauge registers a gauge.
func (r *Registry) NewGauge(name, help string, labels ...string) *Gauge {
	g := &Gauge{base: base{metric: name, help: help, kind: "gauge", labelNames: labels}, values: map[string]*series{}}
	r.add(g)
	return g
}

// Set sets the gauge for the label values.
func (g *Gauge) Set(v float64, labels ...string) {
	k := g.key(labels)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.values[k] = &series{labels: append([]string(nil), labels...), value: v}
}

func (g *Gauge) write(w io.Writer) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.header(w)
	for _, k := range sortedKeys(g.values) {
		s := g.values[k]
		fmt.Fprintf(w, "%s%s %s\n", g.metric, labelText(g.labelNames, s.labels), number(s.value))
	}
}

// --- histogram -----------------------------------------------------------------

// Histogram counts observations into cumulative buckets per label set.
type Histogram struct {
	base
	buckets []float64
	values  map[string]*hseries
}

type hseries struct {
	labels []string
	counts []uint64 // per bucket, non-cumulative
	count  uint64
	sum    float64
}

// NewHistogram registers a histogram with the given upper bounds (ascending).
func (r *Registry) NewHistogram(name, help string, buckets []float64, labels ...string) *Histogram {
	h := &Histogram{
		base:    base{metric: name, help: help, kind: "histogram", labelNames: labels},
		buckets: append([]float64(nil), buckets...),
		values:  map[string]*hseries{},
	}
	r.add(h)
	return h
}

// Observe records one value.
func (h *Histogram) Observe(v float64, labels ...string) {
	k := h.key(labels)
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.values[k]
	if !ok {
		s = &hseries{labels: append([]string(nil), labels...), counts: make([]uint64, len(h.buckets))}
		h.values[k] = s
	}
	for i, ub := range h.buckets {
		if v <= ub {
			s.counts[i]++
			break
		}
	}
	s.count++
	s.sum += v
}

func (h *Histogram) write(w io.Writer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.header(w)
	for _, k := range sortedKeys(h.values) {
		s := h.values[k]
		var cum uint64
		for i, ub := range h.buckets {
			cum += s.counts[i]
			fmt.Fprintf(w, "%s_bucket%s %d\n", h.metric, labelText(h.labelNames, s.labels, `le="`+number(ub)+`"`), cum)
		}
		fmt.Fprintf(w, "%s_bucket%s %d\n", h.metric, labelText(h.labelNames, s.labels, `le="+Inf"`), s.count)
		fmt.Fprintf(w, "%s_sum%s %s\n", h.metric, labelText(h.labelNames, s.labels), number(s.sum))
		fmt.Fprintf(w, "%s_count%s %d\n", h.metric, labelText(h.labelNames, s.labels), s.count)
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
