package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Metrics struct {
	registry            *prometheus.Registry
	httpRequestsTotal   *prometheus.CounterVec
	httpDurationSeconds *prometheus.HistogramVec
	rateLimitedTotal    *prometheus.CounterVec
	redirectsTotal      *prometheus.CounterVec
	adminAuthFailures   prometheus.Counter
}

func New() *Metrics {
	registry := prometheus.NewRegistry()

	m := &Metrics{
		registry: registry,
		httpRequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "i8sl",
			Name:      "http_requests_total",
			Help:      "Total number of processed HTTP requests.",
		}, []string{"route", "method", "status"}),
		httpDurationSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "i8sl",
			Name:      "http_request_duration_seconds",
			Help:      "HTTP request duration in seconds.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"route", "method"}),
		rateLimitedTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "i8sl",
			Name:      "rate_limited_total",
			Help:      "Total number of rate-limited requests.",
		}, []string{"route"}),
		redirectsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "i8sl",
			Name:      "redirect_results_total",
			Help:      "Total number of redirect resolution attempts by result.",
		}, []string{"result"}),
		adminAuthFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "i8sl",
			Name:      "admin_auth_failures_total",
			Help:      "Total number of failed admin authentication attempts.",
		}),
	}

	registry.MustRegister(
		m.httpRequestsTotal,
		m.httpDurationSeconds,
		m.rateLimitedTotal,
		m.redirectsTotal,
		m.adminAuthFailures,
		prometheus.NewGoCollector(),
		prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}),
	)

	return m
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

func (m *Metrics) ObserveHTTPRequest(route, method string, status int, duration time.Duration) {
	m.httpRequestsTotal.WithLabelValues(route, method, strconv.Itoa(status)).Inc()
	m.httpDurationSeconds.WithLabelValues(route, method).Observe(duration.Seconds())
}

func (m *Metrics) IncRateLimited(route string) {
	m.rateLimitedTotal.WithLabelValues(route).Inc()
}

func (m *Metrics) IncRedirect(result string) {
	m.redirectsTotal.WithLabelValues(result).Inc()
}

func (m *Metrics) IncAdminAuthFailure() {
	m.adminAuthFailures.Inc()
}
