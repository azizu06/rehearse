// Package metrics exports low-cardinality Prometheus metrics derived from
// committed drill run state-machine changes.
package metrics

import (
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/azizu06/rehearse/internal/drill"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Labels are restricted to the closed state-machine vocabularies below so a
// plan name, run ID, path, or secret can never become a metric dimension.
var (
	outcomes = []drill.Outcome{
		drill.OutcomeSucceeded,
		drill.OutcomeFailed,
		drill.OutcomeCancelled,
		drill.OutcomeTimedOut,
	}
	stages = []drill.Stage{
		drill.StageQueued,
		drill.StagePreflight,
		drill.StageAcquire,
		drill.StageRestore,
		drill.StageBoot,
		drill.StageProbe,
		drill.StageReport,
		drill.StageCleanup,
	}
)

// Recorder owns the Rehearse metrics registry.
type Recorder struct {
	registry        *prometheus.Registry
	runs            *prometheus.CounterVec
	stageDuration   *prometheus.HistogramVec
	cleanupFailures prometheus.Counter
	lastSuccess     prometheus.Gauge

	mu              sync.Mutex
	lastSuccessTime time.Time
	// retrying marks runs whose cleanup retry this recorder saw begin, until
	// the retry result arrives. Run IDs stay in memory and never become labels.
	retrying map[string]bool
}

// New creates a recorder with process, Go runtime, and drill metrics
// registered. Every closed label value is pre-initialized to zero so
// dashboards see stable series before the first drill.
func New() *Recorder {
	recorder := &Recorder{
		registry: prometheus.NewRegistry(),
		retrying: map[string]bool{},
		runs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "rehearse_drill_runs_total",
			Help: "Drill runs that reached an execution outcome.",
		}, []string{"outcome"}),
		stageDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "rehearse_drill_stage_duration_seconds",
			Help:    "Time a drill run spent in each state-machine stage.",
			Buckets: []float64{0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600, 1800, 3600, 7200, 14400},
		}, []string{"stage"}),
		cleanupFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "rehearse_drill_cleanup_failures_total",
			Help: "Cleanup attempts that failed and left resources for reconciliation.",
		}),
		lastSuccess: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "rehearse_drill_last_success_timestamp_seconds",
			Help: "Unix time of the most recent succeeded drill outcome seen by this process, or 0.",
		}),
	}
	for _, outcome := range outcomes {
		recorder.runs.WithLabelValues(string(outcome))
	}
	for _, stage := range stages {
		recorder.stageDuration.WithLabelValues(string(stage))
	}
	recorder.registry.MustRegister(
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		collectors.NewGoCollector(),
		recorder.runs,
		recorder.stageDuration,
		recorder.cleanupFailures,
		recorder.lastSuccess,
	)
	return recorder
}

// Gatherer exposes the registry for scraping and tests.
func (recorder *Recorder) Gatherer() prometheus.Gatherer {
	return recorder.registry
}

// Handler serves the registry in the Prometheus exposition format.
func (recorder *Recorder) Handler() http.Handler {
	return promhttp.HandlerFor(recorder.registry, promhttp.HandlerOpts{})
}

// ObserveRunChange updates drill metrics from one committed state-machine
// change. before is the projection prior to the change (zero for a new run)
// and after is the projection the change produced.
func (recorder *Recorder) ObserveRunChange(before, after drill.Run, event drill.Event) {
	switch event.Kind {
	case drill.EventStageStarted, drill.EventRunSucceeded, drill.EventRunFailed,
		drill.EventRunCancelled, drill.EventRunTimedOut:
		recorder.observeStageEnd(before, event.OccurredAt)
	case drill.EventCleanupSucceeded, drill.EventCleanupFailed:
		recorder.observeCleanupEnd(before, event.OccurredAt)
	case drill.EventReconciliationRequired:
		if before.Cleanup == drill.CleanupFailed && after.Cleanup == drill.CleanupPending {
			recorder.mu.Lock()
			recorder.retrying[after.ID] = true
			recorder.mu.Unlock()
		}
	}
	switch event.Kind {
	case drill.EventRunSucceeded, drill.EventRunFailed, drill.EventRunCancelled, drill.EventRunTimedOut:
		if slices.Contains(outcomes, after.Outcome) {
			recorder.runs.WithLabelValues(string(after.Outcome)).Inc()
		}
		if after.Outcome == drill.OutcomeSucceeded {
			recorder.recordSuccess(event.OccurredAt)
		}
	case drill.EventCleanupFailed:
		recorder.cleanupFailures.Inc()
	}
}

// observeStageEnd records how long before.Stage lasted. The projection's
// UpdatedAt marks the stage start unless restart reconciliation touched the
// run, in which case the interval spans a process restart and is skipped.
func (recorder *Recorder) observeStageEnd(before drill.Run, endedAt time.Time) {
	if before.NeedsReconciliation || before.UpdatedAt.IsZero() || !slices.Contains(stages, before.Stage) {
		return
	}
	elapsed := endedAt.Sub(before.UpdatedAt)
	if elapsed < 0 {
		return
	}
	recorder.stageDuration.WithLabelValues(string(before.Stage)).Observe(elapsed.Seconds())
}

// observeCleanupEnd measures a cleanup attempt. A retry this recorder saw
// begin started at the retry event, which is the projection's UpdatedAt, so it
// is measured even though the run still awaits reconciliation. Any other
// reconciled interval spans a restart and is skipped by observeStageEnd.
func (recorder *Recorder) observeCleanupEnd(before drill.Run, endedAt time.Time) {
	recorder.mu.Lock()
	retried := recorder.retrying[before.ID]
	delete(recorder.retrying, before.ID)
	recorder.mu.Unlock()
	if retried {
		before.NeedsReconciliation = false
	}
	recorder.observeStageEnd(before, endedAt)
}

func (recorder *Recorder) recordSuccess(at time.Time) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if at.After(recorder.lastSuccessTime) {
		recorder.lastSuccessTime = at
		recorder.lastSuccess.Set(float64(at.UnixNano()) / float64(time.Second))
	}
}
