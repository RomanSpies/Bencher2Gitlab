package app

import (
	"slices"
	"strings"
	"testing"

	"github.com/RomanSpies/Bencher2Gitlab/internal/gitlab/gitlabtest"
	"github.com/RomanSpies/Bencher2Gitlab/internal/report"
)

const secondActiveAlert = "\"limit\": \"lower\",\n      \"status\": \"active\""

func subsetVariant(t *testing.T) string {
	t.Helper()
	return fixtureVariant(t, secondActiveAlert, "\"limit\": \"lower\",\n      \"status\": \"dismissed\"", 1)
}

func newThreadHarness(t *testing.T, reportPath string) *harness {
	t.Helper()
	h := newHarness(t, reportPath)
	h.cfg.Thread = true
	h.cfg.CIID = "bench"
	return h
}

func (h *harness) onlyThread(t *testing.T) gitlabtest.Discussion {
	t.Helper()
	ds := h.fake.Discussions()
	if len(ds) != 1 {
		t.Fatalf("discussions = %d, want exactly 1", len(ds))
	}
	if ds[0].IndividualNote {
		t.Fatalf("discussion %s is a plain note, want a resolvable thread", ds[0].ID)
	}
	return ds[0]
}

func (h *harness) runExpect(t *testing.T, reportPath string, want int) {
	t.Helper()
	h.cfg.ReportPath = reportPath
	h.stderr.Reset()
	if code := h.run(t); code != want {
		t.Fatalf("exit = %d, want %d, stderr: %s", code, want, h.stderr.String())
	}
}

func TestThreadCreatedOpenWithActiveAlerts(t *testing.T) {
	h := newThreadHarness(t, fullFixture)
	h.runExpect(t, fullFixture, ExitAlerts)

	d := h.onlyThread(t)
	if d.Resolved() {
		t.Error("thread with active alerts must stay open")
	}
	if !strings.Contains(d.Notes[0].Body, "2 active alerts") || !alertSetPattern.MatchString(d.Notes[0].Body) {
		t.Errorf("thread body missing report or alert set marker:\n%s", d.Notes[0].Body)
	}
}

func TestThreadCreatedResolvedWithoutAlerts(t *testing.T) {
	h := newThreadHarness(t, noAlertsFixture)
	h.runExpect(t, noAlertsFixture, ExitOK)

	if d := h.onlyThread(t); !d.Resolved() {
		t.Error("green report must produce a resolved thread so it never blocks the merge")
	}
	if !strings.Contains(h.stderr.String(), "resolved thread") {
		t.Errorf("stderr = %q", h.stderr.String())
	}
}

func TestThreadAutoModeWithoutExistingThreadPostsNothing(t *testing.T) {
	h := newThreadHarness(t, noAlertsFixture)
	h.cfg.OnZeroAlerts = ZeroAuto
	h.runExpect(t, noAlertsFixture, ExitOK)
	if n := len(h.fake.Discussions()); n != 0 {
		t.Errorf("discussions = %d, want 0", n)
	}
}

func TestThreadResolvesItselfWhenAlertsClear(t *testing.T) {
	h := newThreadHarness(t, fullFixture)
	h.runExpect(t, fullFixture, ExitAlerts)
	h.runExpect(t, noAlertsFixture, ExitOK)

	d := h.onlyThread(t)
	if !d.Resolved() {
		t.Error("thread must resolve once no alert is active")
	}
	if !strings.Contains(d.Notes[0].Body, "no alerts ✅") {
		t.Errorf("body not updated to green:\n%s", d.Notes[0].Body)
	}
}

func TestThreadReopensWhenClearedAlertsReturn(t *testing.T) {
	h := newThreadHarness(t, fullFixture)
	h.runExpect(t, fullFixture, ExitAlerts)
	h.runExpect(t, noAlertsFixture, ExitOK)
	h.runExpect(t, fullFixture, ExitAlerts)

	if h.onlyThread(t).Resolved() {
		t.Error("auto-resolved thread must reopen when regressions come back, nobody acknowledged them")
	}
	if !strings.Contains(h.stderr.String(), "reopened thread") {
		t.Errorf("stderr = %q", h.stderr.String())
	}
}

func TestThreadAcknowledgedStaysResolved(t *testing.T) {
	h := newThreadHarness(t, fullFixture)
	h.runExpect(t, fullFixture, ExitAlerts)
	h.fake.SetResolved(h.onlyThread(t).ID, true)

	h.runExpect(t, fullFixture, ExitAlerts)

	d := h.onlyThread(t)
	if !d.Resolved() {
		t.Error("reviewer acknowledgement must survive a rerun with the same alerts")
	}
	if !strings.Contains(h.stderr.String(), "stays resolved") {
		t.Errorf("stderr = %q", h.stderr.String())
	}
	if !strings.Contains(d.Notes[0].Body, "2 active alerts") {
		t.Errorf("body must still be refreshed:\n%s", d.Notes[0].Body)
	}
}

func TestThreadAcknowledgedReopensOnNewAlert(t *testing.T) {
	subset := subsetVariant(t)
	h := newThreadHarness(t, subset)
	h.runExpect(t, subset, ExitAlerts)
	h.fake.SetResolved(h.onlyThread(t).ID, true)

	h.runExpect(t, fullFixture, ExitAlerts)

	if h.onlyThread(t).Resolved() {
		t.Error("a regression that was not part of the acknowledged set must reopen the thread")
	}
}

func TestThreadAcknowledgementCoversShrinkingAndRegrowingAlerts(t *testing.T) {
	subset := subsetVariant(t)
	h := newThreadHarness(t, fullFixture)
	h.runExpect(t, fullFixture, ExitAlerts)
	h.fake.SetResolved(h.onlyThread(t).ID, true)

	h.runExpect(t, subset, ExitAlerts)
	if !h.onlyThread(t).Resolved() {
		t.Fatal("fewer alerts than acknowledged must keep the thread resolved")
	}
	h.runExpect(t, fullFixture, ExitAlerts)
	if !h.onlyThread(t).Resolved() {
		t.Error("an alert that was acknowledged before must not reopen the thread when it returns")
	}
}

func TestThreadHumanReplyDoesNotBreakLookup(t *testing.T) {
	h := newThreadHarness(t, fullFixture)
	h.runExpect(t, fullFixture, ExitAlerts)
	h.fake.Reply(h.onlyThread(t).ID, "quoting the bot: "+`<!-- bencher2gitlab id="bench" -->`)
	h.runExpect(t, noAlertsFixture, ExitOK)

	d := h.onlyThread(t)
	if len(d.Notes) != 2 {
		t.Fatalf("notes in thread = %d, want 2", len(d.Notes))
	}
	if !strings.Contains(d.Notes[0].Body, "no alerts ✅") {
		t.Errorf("bot note not updated:\n%s", d.Notes[0].Body)
	}
	if !strings.HasPrefix(d.Notes[1].Body, "quoting the bot") {
		t.Errorf("human reply was modified: %q", d.Notes[1].Body)
	}
	if !d.Resolved() {
		t.Error("thread with a human reply must still resolve when alerts clear")
	}
}

func TestThreadModeUpdatesLegacyPlainNote(t *testing.T) {
	h := newThreadHarness(t, fullFixture)
	h.cfg.Thread = false
	h.runExpect(t, fullFixture, ExitAlerts)
	h.cfg.Thread = true
	h.runExpect(t, noAlertsFixture, ExitOK)

	ds := h.fake.Discussions()
	if len(ds) != 1 || !ds[0].IndividualNote {
		t.Fatalf("want the one plain note updated in place, got %+v", ds)
	}
	if !strings.Contains(ds[0].Notes[0].Body, "no alerts ✅") {
		t.Errorf("plain note not updated:\n%s", ds[0].Notes[0].Body)
	}
	if !strings.Contains(h.stderr.String(), "cannot be resolved") {
		t.Errorf("stderr must explain the legacy note: %q", h.stderr.String())
	}
}

func TestNoteModeLeavesThreadResolutionAlone(t *testing.T) {
	h := newThreadHarness(t, fullFixture)
	h.runExpect(t, fullFixture, ExitAlerts)
	h.cfg.Thread = false
	h.runExpect(t, noAlertsFixture, ExitOK)

	d := h.onlyThread(t)
	if d.Resolved() {
		t.Error("note mode must not touch the resolution state")
	}
	if !strings.Contains(d.Notes[0].Body, "no alerts ✅") {
		t.Errorf("thread note body not updated:\n%s", d.Notes[0].Body)
	}
}

func TestThreadDryRunShowsAlertSet(t *testing.T) {
	h := newThreadHarness(t, fullFixture)
	h.cfg.DryRun = true
	h.runExpect(t, fullFixture, ExitAlerts)
	if !alertSetPattern.MatchString(h.stdout.String()) {
		t.Errorf("dry run in thread mode must render the alert set marker:\n%s", h.stdout.String())
	}
	if h.fake.Requests.Load() != 0 {
		t.Errorf("requests = %d, want 0", h.fake.Requests.Load())
	}
}

func TestNextThreadState(t *testing.T) {
	ab := []string{"a", "b"}
	tests := []struct {
		name    string
		prev    *threadState
		current []string
		want    threadState
	}{
		{"new with alerts opens", nil, ab, threadState{Resolved: false, Alerts: ab}},
		{"new without alerts resolves", nil, nil, threadState{Resolved: true}},
		{"open thread tracks current alerts", &threadState{Alerts: []string{"a"}}, ab, threadState{Alerts: ab}},
		{"open thread resolves when alerts clear", &threadState{Alerts: ab}, nil, threadState{Resolved: true}},
		{"acknowledged set unchanged stays resolved", &threadState{Resolved: true, Alerts: ab}, ab, threadState{Resolved: true, Alerts: ab}},
		{"acknowledged superset keeps the acknowledged set", &threadState{Resolved: true, Alerts: ab}, []string{"b"}, threadState{Resolved: true, Alerts: ab}},
		{"new alert reopens with current set", &threadState{Resolved: true, Alerts: ab}, []string{"a", "c"}, threadState{Alerts: []string{"a", "c"}}},
		{"resolved without stored set reopens on any alert", &threadState{Resolved: true}, []string{"a"}, threadState{Alerts: []string{"a"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nextThreadState(tt.prev, tt.current)
			if got.Resolved != tt.want.Resolved || !slices.Equal(got.Alerts, tt.want.Alerts) {
				t.Errorf("nextThreadState = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestAlertFingerprints(t *testing.T) {
	alert := func(bench, measure, limit, status string, iter int) report.Alert {
		return report.Alert{
			Iteration: iter,
			Benchmark: report.NameSlug{Slug: bench},
			Threshold: report.Threshold{Measure: report.Measure{Slug: measure}},
			Limit:     limit,
			Status:    status,
		}
	}
	rep := &report.Report{Alerts: []report.Alert{
		alert("b2", "latency", "upper", "active", 0),
		alert("b1", "latency", "upper", "active", 0),
		alert("b1", "latency", "upper", "active", 1),
		alert("b1", "latency", "lower", "active", 0),
		alert("b3", "latency", "upper", "silenced", 0),
	}}

	got := alertFingerprints(rep)
	if len(got) != 3 {
		t.Fatalf("fingerprints = %v, want 3 (iterations collapse, inactive alerts ignored)", got)
	}
	if !slices.IsSorted(got) {
		t.Errorf("fingerprints must be sorted for subset checks: %v", got)
	}
	for _, fp := range got {
		if len(fp) != 12 {
			t.Errorf("fingerprint %q has length %d, want 12", fp, len(fp))
		}
	}

	slices.Reverse(rep.Alerts)
	if again := alertFingerprints(rep); !slices.Equal(got, again) {
		t.Errorf("fingerprints depend on alert order: %v vs %v", got, again)
	}
}

func TestAlertSetRoundTrip(t *testing.T) {
	state := threadState{Alerts: []string{"0a1b2c3d4e5f", "ffeeddccbbaa"}}
	body := "<!-- bencher2gitlab id=\"x\" -->\n" + state.marker() + "\n## Bencher Report"
	if got := parseAlertSet(body); !slices.Equal(got, state.Alerts) {
		t.Errorf("parseAlertSet = %v, want %v", got, state.Alerts)
	}
	if got := parseAlertSet("no marker here"); got != nil {
		t.Errorf("parseAlertSet without marker = %v, want nil", got)
	}
	if got := parseAlertSet(threadState{}.marker()); len(got) != 0 {
		t.Errorf("empty set = %v, want empty", got)
	}
}
