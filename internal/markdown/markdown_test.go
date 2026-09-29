package markdown

import (
	"flag"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RomanSpies/Bencher2Gitlab/internal/report"
)

var update = flag.Bool("update", false, "rewrite golden files")

func loadReport(t *testing.T, name string) *report.Report {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "report", "testdata", name))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()
	rep, err := report.Parse(f)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return rep
}

func TestRenderGolden(t *testing.T) {
	tests := []struct {
		fixture string
		golden  string
	}{
		{"full.json", "full.golden.md"},
		{"no_alerts.json", "no_alerts.golden.md"},
		{"minimal.json", "minimal.golden.md"},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			rep := loadReport(t, tt.fixture)
			got := Render(rep, Options{Marker: `<!-- bencher2gitlab id="test" -->`})
			goldenPath := filepath.Join("testdata", tt.golden)
			if *update {
				if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
				return
			}
			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read golden (run `go test ./internal/markdown -update` to create): %v", err)
			}
			if got != string(want) {
				t.Errorf("render mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
			}
		})
	}
}

func TestRenderDeltaGuards(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	tests := []struct {
		name     string
		value    float64
		baseline *float64
	}{
		{"zero baseline", 100, f(0)},
		{"absent baseline", 100, nil},
		{"nan baseline", 100, f(math.NaN())},
		{"inf baseline", 100, f(math.Inf(1))},
		{"nan value", math.NaN(), f(50)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rep := &report.Report{
				UUID:    "u",
				Project: report.NameSlug{Name: "p", Slug: "p"},
				Testbed: report.NameSlug{Name: "tb", Slug: "tb"},
				Alerts: []report.Alert{{
					Status:    "active",
					Benchmark: report.NameSlug{Name: "b"},
					Metric:    report.Metric{Value: tt.value},
					Boundary:  report.Boundary{Baseline: tt.baseline},
					Limit:     "upper",
				}},
			}
			got := Render(rep, Options{})
			if strings.Contains(got, "%)") && strings.Contains(got, "(+") {
				t.Errorf("unexpected delta in output:\n%s", got)
			}
		})
	}
}

func TestRenderEscaping(t *testing.T) {
	rep := &report.Report{
		UUID:    "u",
		Project: report.NameSlug{Name: "p", Slug: "p"},
		Testbed: report.NameSlug{Name: "tb", Slug: "tb"},
		Alerts: []report.Alert{{
			Status:    "active",
			Benchmark: report.NameSlug{Name: "evil|name`with`ticks"},
			Metric:    report.Metric{Value: 1},
			Limit:     "upper",
		}},
	}
	got := Render(rep, Options{})
	if !strings.Contains(got, `evil\|name`) {
		t.Errorf("pipe not escaped:\n%s", got)
	}
	if strings.Contains(got, "`with`") {
		t.Errorf("backticks not stripped from cell:\n%s", got)
	}
}

func TestRenderTruncation(t *testing.T) {
	const marker = `<!-- bencher2gitlab id="trunc" -->`
	rep := &report.Report{
		UUID:    "u",
		Project: report.NameSlug{Name: "proj", Slug: "proj"},
		Branch:  report.NameSlug{Name: "br", Slug: "br"},
		Testbed: report.NameSlug{Name: "tb", Slug: "tb"},
	}
	for i := 0; i < 50; i++ {
		rep.Alerts = append(rep.Alerts, report.Alert{
			Status:    "active",
			Benchmark: report.NameSlug{Name: strings.Repeat("x", 40)},
			Metric:    report.Metric{Value: float64(i)},
			Limit:     "upper",
		})
	}
	got := Render(rep, Options{Marker: marker, MaxBytes: 1500})
	if len(got) > 1500 {
		t.Errorf("body size %d exceeds MaxBytes", len(got))
	}
	if !strings.HasPrefix(got, marker) {
		t.Error("marker did not survive truncation")
	}
	if !strings.Contains(got, "more alert") {
		t.Errorf("missing truncation trailer:\n%s", got)
	}
}

func TestRenderDefaultLimitKeepsNotesReadable(t *testing.T) {
	rep := &report.Report{
		UUID:    "u",
		Project: report.NameSlug{Name: "proj", Slug: "proj"},
		Testbed: report.NameSlug{Name: "tb", Slug: "tb"},
	}
	for i := 0; i < 2000; i++ {
		rep.Alerts = append(rep.Alerts, report.Alert{
			Status:    "active",
			Benchmark: report.NameSlug{Name: strings.Repeat("b", 60)},
			Metric:    report.Metric{Value: float64(i)},
			Limit:     "upper",
		})
	}
	got := Render(rep, Options{})
	if len(got) > 64<<10 {
		t.Errorf("body size %d exceeds the 64 KiB default", len(got))
	}
	if !strings.Contains(got, "more alerts — see the [full report]") {
		t.Errorf("missing truncation trailer pointing to Bencher")
	}
}

func TestRenderMultiIteration(t *testing.T) {
	alert := report.Alert{
		Status:    "active",
		Iteration: 1,
		Benchmark: report.NameSlug{Name: "bench"},
		Metric:    report.Metric{Value: 1},
		Limit:     "upper",
	}
	base := report.Report{
		UUID:    "u",
		Project: report.NameSlug{Name: "p", Slug: "p"},
		Testbed: report.NameSlug{Name: "tb", Slug: "tb"},
		Alerts:  []report.Alert{alert},
	}

	single := base
	single.Results = [][]report.Result{{}}
	if got := Render(&single, Options{}); strings.Contains(got, "(iter ") {
		t.Errorf("single iteration must not show iter suffix:\n%s", got)
	}

	multi := base
	multi.Results = [][]report.Result{{}, {}}
	if got := Render(&multi, Options{}); !strings.Contains(got, "`bench (iter 1)`") {
		t.Errorf("multi iteration must show iter suffix:\n%s", got)
	}
}
