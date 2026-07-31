package main

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type Metrics struct {
	RequestsTotal *prometheus.CounterVec
	LatencyMs     *prometheus.HistogramVec
	RefusalsTotal *prometheus.CounterVec
	ErrorsTotal   *prometheus.CounterVec
}

var latencyBuckets = []float64{10, 25, 50, 100, 200, 500, 1000, 2000, 5000, 10000}

func NewMetrics(reg prometheus.Registerer) *Metrics {
	factory := promauto.With(reg)
	return &Metrics{
		RequestsTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "trafficgen_requests_total",
			Help: "Total requests sent, by prompt type, variant, and status (ok|refused|error).",
		}, []string{"prompt_type", "variant", "status"}),

		LatencyMs: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "trafficgen_latency_ms",
			Help:    "End-to-end round-trip latency in milliseconds.",
			Buckets: latencyBuckets,
		}, []string{"prompt_type", "variant"}),

		RefusalsTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "trafficgen_refusals_total",
			Help: "Responses flagged as refusals by the worker.",
		}, []string{"prompt_type", "variant"}),

		ErrorsTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "trafficgen_errors_total",
			Help: "Failed requests, by error kind (transport|worker).",
		}, []string{"error_kind"}),
	}
}

func (m *Metrics) RecordOK(promptType, variant string, wallMs float64) {
	m.RequestsTotal.WithLabelValues(promptType, variant, "ok").Inc()
	m.LatencyMs.WithLabelValues(promptType, variant).Observe(wallMs)
}

func (m *Metrics) RecordRefusal(promptType, variant string, wallMs float64) {
	m.RequestsTotal.WithLabelValues(promptType, variant, "refused").Inc()
	m.LatencyMs.WithLabelValues(promptType, variant).Observe(wallMs)
	m.RefusalsTotal.WithLabelValues(promptType, variant).Inc()
}

func (m *Metrics) RecordError(promptType, variant, kind string) {
	m.RequestsTotal.WithLabelValues(promptType, variant, "error").Inc()
	m.ErrorsTotal.WithLabelValues(kind).Inc()
}
