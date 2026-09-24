package metrics_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/azizu06/rehearse/internal/metrics"
)

const observabilityDir = "../../deploy/observability"

var rehearseMetricName = regexp.MustCompile(`\brehearse_[a-z0-9_]+`)

type grafanaDashboard struct {
	Title  string `json:"title"`
	UID    string `json:"uid"`
	Panels []struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Datasource  struct {
			UID string `json:"uid"`
		} `json:"datasource"`
		Targets []struct {
			Expr string `json:"expr"`
		} `json:"targets"`
	} `json:"panels"`
}

func TestGrafanaDashboardQueriesOnlyRegisteredMetrics(t *testing.T) {
	t.Parallel()

	encoded, err := os.ReadFile(filepath.Join(observabilityDir, "grafana/dashboards/rehearse.json"))
	if err != nil {
		t.Fatalf("read dashboard: %v", err)
	}
	var dashboard grafanaDashboard
	if err := json.Unmarshal(encoded, &dashboard); err != nil {
		t.Fatalf("parse dashboard: %v", err)
	}
	if dashboard.Title == "" || dashboard.UID == "" || len(dashboard.Panels) == 0 {
		t.Fatalf("dashboard needs a title, uid, and panels: %+v", dashboard)
	}
	datasources, err := os.ReadFile(filepath.Join(observabilityDir, "grafana/provisioning/datasources/prometheus.yaml"))
	if err != nil {
		t.Fatalf("read datasource provisioning: %v", err)
	}

	registered := map[string]bool{}
	for name := range gather(t, metrics.New()) {
		if strings.HasPrefix(name, "rehearse_") {
			registered[name] = false
		}
	}
	for _, panel := range dashboard.Panels {
		if panel.Title == "" || panel.Description == "" || len(panel.Targets) == 0 {
			t.Fatalf("panel %q needs a title, an explanatory description, and a query", panel.Title)
		}
		if !strings.Contains(string(datasources), "uid: "+panel.Datasource.UID) {
			t.Fatalf("panel %q datasource uid %q is not provisioned", panel.Title, panel.Datasource.UID)
		}
		for _, target := range panel.Targets {
			for _, name := range rehearseMetricName.FindAllString(target.Expr, -1) {
				family := histogramFamily(name, registered)
				if _, ok := registered[family]; !ok {
					t.Fatalf("panel %q queries unregistered metric %s", panel.Title, name)
				}
				registered[family] = true
			}
		}
	}
	for name, used := range registered {
		if !used {
			t.Fatalf("dashboard does not chart registered metric %s", name)
		}
	}
}

// histogramFamily maps a histogram series name back to its registered family.
func histogramFamily(name string, registered map[string]bool) string {
	for _, suffix := range []string{"_bucket", "_sum", "_count"} {
		if family, ok := strings.CutSuffix(name, suffix); ok {
			if _, known := registered[family]; known {
				return family
			}
		}
	}
	return name
}
