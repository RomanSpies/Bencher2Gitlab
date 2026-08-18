package report

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustParse(t *testing.T, name string) *Report {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()
	rep, err := Parse(f)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return rep
}

func TestParseFullMetadata(t *testing.T) {
	rep := mustParse(t, "full.json")
	if rep.UUID != "7d3f9b2c-4a1e-4c8b-9f0d-2e5a6b7c8d9e" {
		t.Errorf("uuid = %q", rep.UUID)
	}
	if rep.Project.Slug != "bankingsync" || rep.Project.Name != "BankingSync" {
		t.Errorf("project = %+v", rep.Project)
	}
	if rep.Branch.Slug != "feature-ebics-batch" {
		t.Errorf("branch = %+v", rep.Branch)
	}
	if rep.Testbed.Name != "gcc" {
		t.Errorf("testbed = %+v", rep.Testbed)
	}
	if rep.Adapter != "go_bench" {
		t.Errorf("adapter = %q", rep.Adapter)
	}
}

func TestParseFullResults(t *testing.T) {
	rep := mustParse(t, "full.json")
	if len(rep.Results) != 2 {
		t.Fatalf("iterations = %d, want 2", len(rep.Results))
	}
	if len(rep.Results[0]) != 2 || len(rep.Results[1]) != 1 {
		t.Errorf("results per iteration = %d/%d, want 2/1", len(rep.Results[0]), len(rep.Results[1]))
	}
	rm := rep.Results[0][0].Measures[0]
	if rm.Measure.Units != "nanoseconds (ns)" {
		t.Errorf("units = %q", rm.Measure.Units)
	}
	if rm.Metric.Value != 1234.56 {
		t.Errorf("value = %v", rm.Metric.Value)
	}
	if rm.Metric.LowerValue == nil || *rm.Metric.LowerValue != 1200.0 {
		t.Errorf("lower_value = %v", rm.Metric.LowerValue)
	}
	if rm.Boundary == nil || rm.Boundary.Baseline == nil || *rm.Boundary.Baseline != 985.2 {
		t.Errorf("boundary = %+v", rm.Boundary)
	}
}

func TestParseFullAlerts(t *testing.T) {
	rep := mustParse(t, "full.json")
	if len(rep.Alerts) != 3 {
		t.Fatalf("alerts = %d, want 3", len(rep.Alerts))
	}
	a := rep.Alerts[0]
	if a.Limit != LimitUpper {
		t.Errorf("alerts[0].limit = %q, want %q", a.Limit, LimitUpper)
	}
	if a.Status != StatusActive {
		t.Errorf("alerts[0].status = %q", a.Status)
	}
	if a.Threshold.Measure.Name != "Latency" || a.Threshold.Model.Test != "t_test" {
		t.Errorf("alerts[0].threshold = %+v", a.Threshold)
	}
	if a.Boundary.UpperLimit == nil || *a.Boundary.UpperLimit != 1080.0 {
		t.Errorf("alerts[0].boundary.upper_limit = %v", a.Boundary.UpperLimit)
	}
	if rep.Alerts[1].Limit != LimitLower || rep.Alerts[1].Iteration != 1 {
		t.Errorf("alerts[1] = limit %q, iteration %d", rep.Alerts[1].Limit, rep.Alerts[1].Iteration)
	}
}

func TestParseNoAlerts(t *testing.T) {
	rep := mustParse(t, "no_alerts.json")
	if len(rep.Alerts) != 0 {
		t.Errorf("alerts = %d, want 0", len(rep.Alerts))
	}
	if rep.Testbed.Name != "clang" {
		t.Errorf("testbed = %+v", rep.Testbed)
	}
}

func TestParseMinimal(t *testing.T) {
	rep := mustParse(t, "minimal.json")
	rm := rep.Results[0][0].Measures[0]
	if rm.Boundary != nil {
		t.Errorf("boundary = %+v, want nil", rm.Boundary)
	}
	if rm.Metric.LowerValue != nil || rm.Metric.UpperValue != nil {
		t.Errorf("lower/upper value should be nil: %+v", rm.Metric)
	}
	a := rep.Alerts[0]
	if a.Boundary.Baseline != nil || a.Boundary.LowerLimit != nil {
		t.Errorf("alert boundary = %+v", a.Boundary)
	}
	if a.Threshold.Measure.Name != "" {
		t.Errorf("missing threshold should decode to zero value, got %+v", a.Threshold)
	}
}

func TestParseErrors(t *testing.T) {
	invalid, err := os.ReadFile(filepath.Join("testdata", "invalid.json"))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		input   string
		wantSub string
	}{
		{"invalid json", string(invalid), "parsing bencher report"},
		{"empty", "  \n", "empty input"},
		{"top level array", `[{"uuid":"x"}]`, "expected a single JsonReport object"},
		{"not a report", `{"foo": 1}`, "--format json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tt.input))
			if err == nil || !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("err = %v, want substring %q", err, tt.wantSub)
			}
		})
	}
}

func TestActiveAlerts(t *testing.T) {
	rep := &Report{Alerts: []Alert{
		{UUID: "1", Status: "active"},
		{UUID: "2", Status: "dismissed"},
		{UUID: "3", Status: "silenced"},
		{UUID: "4", Status: "active"},
	}}
	active := rep.ActiveAlerts()
	if len(active) != 2 || active[0].UUID != "1" || active[1].UUID != "4" {
		t.Errorf("ActiveAlerts = %+v", active)
	}
	if got := (&Report{}).ActiveAlerts(); len(got) != 0 {
		t.Errorf("empty report: %+v", got)
	}
}

func TestPermalinkURL(t *testing.T) {
	rep := &Report{UUID: "abc", Project: NameSlug{Slug: "proj"}}
	want := "https://bencher.dev/perf/proj/reports/abc"
	if got := rep.PermalinkURL("https://bencher.dev"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := rep.PermalinkURL("https://bencher.dev/"); got != want {
		t.Errorf("trailing slash: got %q, want %q", got, want)
	}
}
