// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package metrics

import (
	"sync"
	"time"

	runnerv1 "code.gitea.io/actions-proto-go/runner/v1"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Namespace is the Prometheus namespace for all gitea-runner metrics.
const Namespace = "gitea_runner"

// Label value constants for Prometheus metrics.
// Using constants prevents typos from silently creating new time-series.
//
// LabelResult* values are used on metrics with label key "result" (RPC outcomes).
// LabelStatus* values are used on metrics with label key "status" (job outcomes).
const (
	LabelResultTask    = "task"
	LabelResultEmpty   = "empty"
	LabelResultError   = "error"
	LabelResultSuccess = "success"

	LabelMethodFetchTask  = "FetchTask"
	LabelMethodUpdateLog  = "UpdateLog"
	LabelMethodUpdateTask = "UpdateTask"

	LabelStatusSuccess   = "success"
	LabelStatusFailure   = "failure"
	LabelStatusCancelled = "cancelled"
	LabelStatusSkipped   = "skipped"
	LabelStatusUnknown   = "unknown"
)

// rpcDurationBuckets covers the expected latency range for short-running
// UpdateLog / UpdateTask RPCs. FetchTask uses its own buckets (it has a 10s tail).
var rpcDurationBuckets = []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5}

// ResultToStatusLabel maps a runnerv1.Result to the "status" label value used on job metrics.
func ResultToStatusLabel(r runnerv1.Result) string {
	switch r {
	case runnerv1.Result_RESULT_SUCCESS:
		return LabelStatusSuccess
	case runnerv1.Result_RESULT_FAILURE:
		return LabelStatusFailure
	case runnerv1.Result_RESULT_CANCELLED:
		return LabelStatusCancelled
	case runnerv1.Result_RESULT_SKIPPED:
		return LabelStatusSkipped
	default:
		return LabelStatusUnknown
	}
}

var (
	RunnerInfo = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: Namespace,
		Name:      "info",
		Help:      "Runner metadata. Always 1. Labels carry version and name.",
	}, []string{"version", "name"})

	RunnerCapacity = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: Namespace,
		Name:      "capacity",
		Help:      "Configured maximum concurrent jobs.",
	})

	PollFetchTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: Namespace,
		Subsystem: "poll",
		Name:      "fetch_total",
		Help:      "Total number of FetchTask RPCs by result (task, empty, error).",
	}, []string{"result"})

	PollFetchDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: Namespace,
		Subsystem: "poll",
		Name:      "fetch_duration_seconds",
		Help:      "Latency of FetchTask RPCs, excluding expected long-poll timeouts.",
		Buckets:   []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10},
	})

	PollBackoffSeconds = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: Namespace,
		Subsystem: "poll",
		Name:      "backoff_seconds",
		Help:      "Last observed polling backoff interval in seconds.",
	})

	JobsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: Namespace,
		Subsystem: "job",
		Name:      "total",
		Help:      "Total jobs processed by status (success, failure, cancelled, skipped, unknown).",
	}, []string{"status"})

	JobDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: Namespace,
		Subsystem: "job",
		Name:      "duration_seconds",
		Help:      "Duration of job execution from start to finish.",
		Buckets:   prometheus.ExponentialBuckets(1, 2, 14), // 1s to ~4.5h
	})

	ReportLogTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: Namespace,
		Subsystem: "report",
		Name:      "log_total",
		Help:      "Total UpdateLog RPCs by result (success, error).",
	}, []string{"result"})

	ReportLogDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: Namespace,
		Subsystem: "report",
		Name:      "log_duration_seconds",
		Help:      "Latency of UpdateLog RPCs.",
		Buckets:   rpcDurationBuckets,
	})

	ReportStateTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: Namespace,
		Subsystem: "report",
		Name:      "state_total",
		Help:      "Total UpdateTask (state) RPCs by result (success, error).",
	}, []string{"result"})

	ReportStateDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: Namespace,
		Subsystem: "report",
		Name:      "state_duration_seconds",
		Help:      "Latency of UpdateTask RPCs.",
		Buckets:   rpcDurationBuckets,
	})

	ReportLogBufferRows = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: Namespace,
		Subsystem: "report",
		Name:      "log_buffer_rows",
		Help:      "Current number of buffered log rows awaiting send.",
	})

	ClientErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: Namespace,
		Subsystem: "client",
		Name:      "errors_total",
		Help:      "Total client RPC errors by method.",
	}, []string{"method"})
)

// Registry is the custom Prometheus registry used by the runner.
var Registry = prometheus.NewRegistry()

var initOnce sync.Once

// Init registers all static metrics and the standard Go/process collectors.
// Safe to call multiple times; only the first call has effect.
func Init() {
	initOnce.Do(func() {
		Registry.MustRegister(
			collectors.NewGoCollector(),
			collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
			RunnerInfo, RunnerCapacity,
			PollFetchTotal, PollFetchDuration, PollBackoffSeconds,
			JobsTotal, JobDuration,
			ReportLogTotal, ReportLogDuration,
			ReportStateTotal, ReportStateDuration, ReportLogBufferRows,
			ClientErrors,
		)
	})
}

// RegisterUptimeFunc registers a GaugeFunc that reports seconds since startTime.
func RegisterUptimeFunc(startTime time.Time) {
	Registry.MustRegister(prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Namespace: Namespace,
			Name:      "uptime_seconds",
			Help:      "Seconds since the runner daemon started.",
		},
		func() float64 { return time.Since(startTime).Seconds() },
	))
}

// RegisterRunningJobsFunc registers GaugeFuncs for the running job count and
// capacity utilisation ratio, evaluated lazily at Prometheus scrape time.
func RegisterRunningJobsFunc(countFn func() int64, capacity int) {
	capF := float64(capacity)
	Registry.MustRegister(prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Namespace: Namespace,
			Subsystem: "job",
			Name:      "running",
			Help:      "Number of jobs currently executing.",
		},
		func() float64 { return float64(countFn()) },
	))
	Registry.MustRegister(prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Namespace: Namespace,
			Subsystem: "job",
			Name:      "capacity_utilization_ratio",
			Help:      "Ratio of running jobs to configured capacity (0.0-1.0).",
		},
		func() float64 {
			if capF <= 0 {
				return 0
			}
			return float64(countFn()) / capF
		},
	))
}
