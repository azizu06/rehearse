package metrics_test

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/azizu06/rehearse/internal/drill"
	"github.com/azizu06/rehearse/internal/journal"
	"github.com/azizu06/rehearse/internal/metrics"
	dto "github.com/prometheus/client_model/go"
)

var _ journal.RunObserver = (*metrics.Recorder)(nil)

func TestRecorderRegistersLowCardinalityDrillMetrics(t *testing.T) {
	t.Parallel()

	families := gather(t, metrics.New())
	wantLabels := map[string][]string{
		"rehearse_drill_runs_total":                     {"outcome"},
		"rehearse_drill_stage_duration_seconds":         {"stage"},
		"rehearse_drill_cleanup_failures_total":         nil,
		"rehearse_drill_last_success_timestamp_seconds": nil,
	}
	for name, labels := range wantLabels {
		family, ok := families[name]
		if !ok {
			t.Fatalf("metric %s is not registered", name)
		}
		for _, metric := range family.GetMetric() {
			if got := labelNames(metric); !equalStrings(got, labels) {
				t.Fatalf("%s labels = %v, want %v", name, got, labels)
			}
		}
	}

	outcomes := labelValues(families["rehearse_drill_runs_total"], "outcome")
	if want := []string{"cancelled", "failed", "succeeded", "timed_out"}; !equalStrings(outcomes, want) {
		t.Fatalf("pre-initialized outcomes = %v, want %v", outcomes, want)
	}
	stages := labelValues(families["rehearse_drill_stage_duration_seconds"], "stage")
	if want := []string{"acquire", "boot", "cleanup", "preflight", "probe", "queued", "report", "restore"}; !equalStrings(stages, want) {
		t.Fatalf("pre-initialized stages = %v, want %v", stages, want)
	}
}

func TestRecorderFollowsStateMachineChanges(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC)
	at := func(seconds int) time.Time { return start.Add(time.Duration(seconds) * time.Second) }
	transition := func(stage drill.Stage, seconds int) step {
		return func(run *drill.Run) (drill.Event, error) { return run.Transition(stage, at(seconds)) }
	}
	outcome := func(outcome drill.Outcome, seconds int) step {
		return func(run *drill.Run) (drill.Event, error) { return run.RecordOutcome(outcome, at(seconds)) }
	}
	cleanup := func(status drill.CleanupStatus, seconds int) step {
		return func(run *drill.Run) (drill.Event, error) { return run.RecordCleanup(status, at(seconds)) }
	}
	restart := func(seconds int) step {
		return func(run *drill.Run) (drill.Event, error) { return run.RequireReconciliation(at(seconds)) }
	}
	retryCleanup := func(seconds int) step {
		return func(run *drill.Run) (drill.Event, error) { return run.BeginCleanupRetry(at(seconds)) }
	}
	tests := []struct {
		name  string
		steps []step
		want  map[string]float64
	}{
		{
			name: "succeeded drill with clean cleanup",
			steps: []step{
				transition(drill.StagePreflight, 1),
				transition(drill.StageAcquire, 2),
				transition(drill.StageRestore, 4),
				transition(drill.StageBoot, 7),
				transition(drill.StageProbe, 11),
				transition(drill.StageReport, 16),
				outcome(drill.OutcomeSucceeded, 22),
				cleanup(drill.CleanupSucceeded, 25),
			},
			want: map[string]float64{
				"runs{succeeded}":  1,
				"count{queued}":    1,
				"sum{queued}":      1,
				"count{preflight}": 1,
				"sum{preflight}":   1,
				"count{acquire}":   1,
				"sum{acquire}":     2,
				"count{restore}":   1,
				"sum{restore}":     3,
				"count{boot}":      1,
				"sum{boot}":        4,
				"count{probe}":     1,
				"sum{probe}":       5,
				"count{report}":    1,
				"sum{report}":      6,
				"count{cleanup}":   1,
				"sum{cleanup}":     3,
				"last_success":     float64(at(22).Unix()),
			},
		},
		{
			name: "failed restore with failed cleanup",
			steps: []step{
				transition(drill.StagePreflight, 1),
				transition(drill.StageAcquire, 2),
				transition(drill.StageRestore, 4),
				outcome(drill.OutcomeFailed, 9),
				cleanup(drill.CleanupFailed, 10),
			},
			want: map[string]float64{
				"runs{failed}":     1,
				"count{queued}":    1,
				"sum{queued}":      1,
				"count{preflight}": 1,
				"sum{preflight}":   1,
				"count{acquire}":   1,
				"sum{acquire}":     2,
				"count{restore}":   1,
				"sum{restore}":     5,
				"count{cleanup}":   1,
				"sum{cleanup}":     1,
				"cleanup_failures": 1,
			},
		},
		{
			name: "restart-interrupted intervals are not observed",
			steps: []step{
				transition(drill.StagePreflight, 1),
				restart(30),
				outcome(drill.OutcomeTimedOut, 31),
				cleanup(drill.CleanupFailed, 33),
				retryCleanup(60),
				cleanup(drill.CleanupSucceeded, 65),
			},
			want: map[string]float64{
				"runs{timed_out}":  1,
				"count{queued}":    1,
				"sum{queued}":      1,
				"count{cleanup}":   1,
				"sum{cleanup}":     2,
				"cleanup_failures": 1,
			},
		},
		{
			name: "cancelled while queued",
			steps: []step{
				outcome(drill.OutcomeCancelled, 3),
			},
			want: map[string]float64{
				"runs{cancelled}": 1,
				"count{queued}":   1,
				"sum{queued}":     3,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			recorder := metrics.New()
			run, created, err := drill.NewRun("run-1", "plan-1", 1, start)
			if err != nil {
				t.Fatalf("NewRun: %v", err)
			}
			recorder.ObserveRunChange(drill.Run{}, run, created)
			for index, apply := range test.steps {
				before := run
				event, err := apply(&run)
				if err != nil {
					t.Fatalf("step %d: %v", index, err)
				}
				recorder.ObserveRunChange(before, run, event)
			}
			if got := snapshot(gather(t, recorder)); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("metrics = %v, want %v", got, test.want)
			}
		})
	}
}

type step func(*drill.Run) (drill.Event, error)

// snapshot flattens the non-zero drill series into comparable keys.
func snapshot(families map[string]*dto.MetricFamily) map[string]float64 {
	values := map[string]float64{}
	put := func(key string, value float64) {
		if value != 0 {
			values[key] = value
		}
	}
	for _, metric := range families["rehearse_drill_runs_total"].GetMetric() {
		put(fmt.Sprintf("runs{%s}", metric.GetLabel()[0].GetValue()), metric.GetCounter().GetValue())
	}
	for _, metric := range families["rehearse_drill_stage_duration_seconds"].GetMetric() {
		stage := metric.GetLabel()[0].GetValue()
		put(fmt.Sprintf("count{%s}", stage), float64(metric.GetHistogram().GetSampleCount()))
		put(fmt.Sprintf("sum{%s}", stage), metric.GetHistogram().GetSampleSum())
	}
	put("cleanup_failures", families["rehearse_drill_cleanup_failures_total"].GetMetric()[0].GetCounter().GetValue())
	put("last_success", families["rehearse_drill_last_success_timestamp_seconds"].GetMetric()[0].GetGauge().GetValue())
	return values
}

func gather(t *testing.T, recorder *metrics.Recorder) map[string]*dto.MetricFamily {
	t.Helper()
	gathered, err := recorder.Gatherer().Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	families := make(map[string]*dto.MetricFamily, len(gathered))
	for _, family := range gathered {
		families[family.GetName()] = family
	}
	return families
}

func labelNames(metric *dto.Metric) []string {
	var names []string
	for _, pair := range metric.GetLabel() {
		names = append(names, pair.GetName())
	}
	return names
}

func labelValues(family *dto.MetricFamily, label string) []string {
	var values []string
	for _, metric := range family.GetMetric() {
		for _, pair := range metric.GetLabel() {
			if pair.GetName() == label {
				values = append(values, pair.GetValue())
			}
		}
	}
	return values
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
