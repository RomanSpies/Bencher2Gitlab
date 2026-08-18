package markdown

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/RomanSpies/Bencher2Gitlab/internal/report"
)

const (
	defaultBencherURL = "https://bencher.dev"
	defaultMaxBytes   = 900_000
	truncationReserve = 256
)

type Options struct {
	Marker     string
	BencherURL string
	MaxBytes   int
}

func Render(rep *report.Report, opts Options) string {
	if opts.BencherURL == "" {
		opts.BencherURL = defaultBencherURL
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = defaultMaxBytes
	}
	active := rep.ActiveAlerts()
	inactive := len(rep.Alerts) - len(active)
	permalink := rep.PermalinkURL(opts.BencherURL)

	var b strings.Builder
	if opts.Marker != "" {
		b.WriteString(opts.Marker)
		b.WriteByte('\n')
	}
	b.WriteString("## Bencher Report [" + escapeCell(rep.Testbed.Name) + "]: ")
	if len(active) == 0 {
		b.WriteString("no alerts ✅")
	} else {
		fmt.Fprintf(&b, "%d active alert%s ⚠️", len(active), plural(len(active)))
	}
	if inactive > 0 {
		fmt.Fprintf(&b, " (%d inactive)", inactive)
	}
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "**Project:** `%s` | **Branch:** `%s` | **Testbed:** `%s` | [Full report](%s)\n",
		escapeCell(rep.Project.Name), escapeCell(rep.Branch.Name), escapeCell(rep.Testbed.Name), permalink)
	if len(active) == 0 {
		return b.String()
	}

	b.WriteString("\n| Testbed | Benchmark | Measure (units) | Value | Lower Boundary | Upper Boundary |\n")
	b.WriteString("| --- | --- | --- | ---: | ---: | ---: |\n")

	multiIteration := len(rep.Results) > 1
	for i, a := range active {
		row := renderRow(rep.Testbed.Name, a, multiIteration)
		if b.Len()+len(row) > opts.MaxBytes-truncationReserve {
			fmt.Fprintf(&b, "\n_… and %d more alert%s — see the [full report](%s)._\n",
				len(active)-i, plural(len(active)-i), permalink)
			return b.String()
		}
		b.WriteString(row)
	}
	return b.String()
}

func renderRow(testbed string, a report.Alert, multiIteration bool) string {
	benchmark := escapeCell(a.Benchmark.Name)
	if multiIteration {
		benchmark += fmt.Sprintf(" (iter %d)", a.Iteration)
	}
	measure := escapeCell(a.Threshold.Measure.Name)
	if units := a.Threshold.Measure.Units; units != "" {
		measure += " (" + escapeCell(units) + ")"
	}
	value := formatNumber(a.Metric.Value)
	if delta, ok := percentDelta(a.Metric.Value, a.Boundary.Baseline); ok {
		value += " " + delta
	}
	return fmt.Sprintf("| `%s` | `%s` | %s | %s | %s | %s |\n",
		escapeCell(testbed), benchmark, measure, value,
		boundaryCell(a.Boundary.LowerLimit, a.Metric.Value, a.Limit == report.LimitLower),
		boundaryCell(a.Boundary.UpperLimit, a.Metric.Value, a.Limit == report.LimitUpper))
}

func boundaryCell(limit *float64, value float64, violated bool) string {
	if limit == nil {
		return "—"
	}
	s := formatNumber(*limit)
	if !violated {
		return s
	}
	if ratio := value / *limit * 100; *limit != 0 && !math.IsNaN(ratio) && !math.IsInf(ratio, 0) {
		s += fmt.Sprintf(" (%.2f%%)", ratio)
	}
	return "**" + s + "** ⚠️"
}

func percentDelta(value float64, baseline *float64) (string, bool) {
	if baseline == nil || *baseline == 0 || math.IsNaN(*baseline) || math.IsInf(*baseline, 0) {
		return "", false
	}
	delta := (value - *baseline) / *baseline * 100
	if math.IsNaN(delta) || math.IsInf(delta, 0) {
		return "", false
	}
	return fmt.Sprintf("(%+.2f%%)", delta), true
}

func formatNumber(f float64) string {
	s := strconv.FormatFloat(f, 'f', 2, 64)
	return strings.TrimSuffix(s, ".00")
}

func escapeCell(s string) string {
	r := strings.NewReplacer("`", "", "|", "\\|", "\n", " ", "\r", " ")
	return r.Replace(s)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
