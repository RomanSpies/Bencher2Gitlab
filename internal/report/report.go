package report

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

type Report struct {
	UUID      string     `json:"uuid"`
	Project   NameSlug   `json:"project"`
	Branch    NameSlug   `json:"branch"`
	Testbed   NameSlug   `json:"testbed"`
	Adapter   string     `json:"adapter"`
	StartTime time.Time  `json:"start_time"`
	EndTime   time.Time  `json:"end_time"`
	Results   [][]Result `json:"results"`
	Alerts    []Alert    `json:"alerts"`
}

type NameSlug struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type Result struct {
	Iteration int             `json:"iteration"`
	Benchmark NameSlug        `json:"benchmark"`
	Measures  []ReportMeasure `json:"measures"`
}

type ReportMeasure struct {
	Measure  Measure   `json:"measure"`
	Metric   Metric    `json:"metric"`
	Boundary *Boundary `json:"boundary,omitempty"`
}

type Measure struct {
	Name  string `json:"name"`
	Slug  string `json:"slug"`
	Units string `json:"units"`
}

type Metric struct {
	Value      float64  `json:"value"`
	LowerValue *float64 `json:"lower_value,omitempty"`
	UpperValue *float64 `json:"upper_value,omitempty"`
}

type Boundary struct {
	Baseline   *float64 `json:"baseline,omitempty"`
	LowerLimit *float64 `json:"lower_limit,omitempty"`
	UpperLimit *float64 `json:"upper_limit,omitempty"`
}

const (
	LimitLower = "lower"
	LimitUpper = "upper"

	StatusActive = "active"
)

type Alert struct {
	UUID      string    `json:"uuid"`
	Iteration int       `json:"iteration"`
	Benchmark NameSlug  `json:"benchmark"`
	Metric    Metric    `json:"metric"`
	Threshold Threshold `json:"threshold"`
	Boundary  Boundary  `json:"boundary"`
	Limit     string    `json:"limit"`
	Status    string    `json:"status"`
}

type Threshold struct {
	Measure Measure `json:"measure"`
	Model   Model   `json:"model"`
}

type Model struct {
	Test string `json:"test"`
}

func Parse(r io.Reader) (*Report, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("reading report: %w", err)
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, errors.New("empty input: expected a bencher JsonReport (did you run bencher with --quiet --format json?)")
	}
	if data[0] == '[' {
		return nil, errors.New("expected a single JsonReport object, got a JSON array (is the input raw benchmark output instead of the bencher report?)")
	}
	var rep Report
	if err := json.Unmarshal(data, &rep); err != nil {
		return nil, fmt.Errorf("parsing bencher report: %w", err)
	}
	if rep.UUID == "" || rep.Project.Slug == "" {
		return nil, errors.New("input is not a bencher JsonReport: missing uuid/project.slug (did you run bencher with --quiet --format json?)")
	}
	return &rep, nil
}

func (r *Report) ActiveAlerts() []Alert {
	var active []Alert
	for _, a := range r.Alerts {
		if a.Status == StatusActive {
			active = append(active, a)
		}
	}
	return active
}

func (r *Report) PermalinkURL(base string) string {
	return strings.TrimSuffix(base, "/") + "/perf/" + r.Project.Slug + "/reports/" + r.UUID
}
