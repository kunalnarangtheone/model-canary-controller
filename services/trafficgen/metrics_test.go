package main

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestMetrics_RecordOK(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

	m.RecordOK("factual", "VARIANT_INCUMBENT", 123.0)
	m.RecordOK("factual", "VARIANT_INCUMBENT", 200.0)

	// Verify counter reached 2.
	expected := `
		# HELP trafficgen_requests_total Total requests sent, by prompt type, variant, and status (ok|refused|error).
		# TYPE trafficgen_requests_total counter
		trafficgen_requests_total{prompt_type="factual",status="ok",variant="VARIANT_INCUMBENT"} 2
	`
	if err := testutil.GatherAndCompare(reg,
		strings.NewReader(expected),
		"trafficgen_requests_total",
	); err != nil {
		t.Error(err)
	}
}

func TestMetrics_RecordRefusal(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

	m.RecordRefusal("reasoning", "VARIANT_CANDIDATE_A", 50.0)

	expected := `
		# HELP trafficgen_refusals_total Responses flagged as refusals by the worker.
		# TYPE trafficgen_refusals_total counter
		trafficgen_refusals_total{prompt_type="reasoning",variant="VARIANT_CANDIDATE_A"} 1
	`
	if err := testutil.GatherAndCompare(reg,
		strings.NewReader(expected),
		"trafficgen_refusals_total",
	); err != nil {
		t.Error(err)
	}
}

func TestMetrics_RecordError(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

	m.RecordError("instruction", "UNKNOWN", "transport")
	m.RecordError("instruction", "UNKNOWN", "worker")
	m.RecordError("factual", "VARIANT_INCUMBENT", "transport")

	expected := `
		# HELP trafficgen_errors_total Failed requests, by error kind (transport|worker).
		# TYPE trafficgen_errors_total counter
		trafficgen_errors_total{error_kind="transport"} 2
		trafficgen_errors_total{error_kind="worker"} 1
	`
	if err := testutil.GatherAndCompare(reg,
		strings.NewReader(expected),
		"trafficgen_errors_total",
	); err != nil {
		t.Error(err)
	}
}
