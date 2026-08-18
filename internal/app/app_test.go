package app

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RomanSpies/Bencher2Gitlab/internal/gitlab/gitlabtest"
)

const (
	fullFixture     = "../report/testdata/full.json"
	noAlertsFixture = "../report/testdata/no_alerts.json"

	gccMarker      = `<!-- bencher2gitlab id="bankingsync/feature-ebics-batch/gcc/go_bench" -->`
	clangMarker    = `<!-- bencher2gitlab id="bankingsync/feature-ebics-batch/clang/go_bench" -->`
	noAlertsMarker = clangMarker
)

type harness struct {
	fake   *gitlabtest.Fake
	cfg    Config
	stdout bytes.Buffer
	stderr bytes.Buffer
}

func newHarness(t *testing.T, reportPath string) *harness {
	t.Helper()
	fake := gitlabtest.New()
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	return &harness{
		fake: fake,
		cfg: Config{
			ReportPath: reportPath,
			GitLabURL:  srv.URL,
			ProjectID:  "42",
			MRIID:      "7",
			Token:      "tok",
			HTTPClient: srv.Client(),
		},
	}
}

func (h *harness) run(t *testing.T) int {
	t.Helper()
	return Run(context.Background(), h.cfg, strings.NewReader(""), &h.stdout, &h.stderr)
}

func clangVariant(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(fullFixture)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.ReplaceAll(data, []byte(`"gcc"`), []byte(`"clang"`))
	path := filepath.Join(t.TempDir(), "full_clang.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunCreatesNote(t *testing.T) {
	h := newHarness(t, fullFixture)
	if code := h.run(t); code != ExitAlerts {
		t.Fatalf("exit = %d, want %d (active alerts), stderr: %s", code, ExitAlerts, h.stderr.String())
	}
	notes := h.fake.Notes()
	if len(notes) != 1 {
		t.Fatalf("notes = %d, want 1", len(notes))
	}
	body := notes[0].Body
	for _, want := range []string{gccMarker, "2 active alerts", "| `gcc` | `BenchmarkParseEBICS", "**1080 (114.31%)** ⚠️"} {
		if !strings.Contains(body, want) {
			t.Errorf("note body missing %q:\n%s", want, body)
		}
	}
	if !strings.Contains(h.stderr.String(), "created note 1 on MR !7") {
		t.Errorf("stderr = %q", h.stderr.String())
	}
}

func TestRunIsIdempotent(t *testing.T) {
	h := newHarness(t, fullFixture)
	h.run(t)
	firstID := h.fake.Notes()[0].ID

	if code := h.run(t); code != ExitAlerts {
		t.Fatalf("second run exit = %d", code)
	}
	notes := h.fake.Notes()
	if len(notes) != 1 {
		t.Fatalf("notes = %d after second run, want 1 (no duplicate)", len(notes))
	}
	if notes[0].ID != firstID {
		t.Errorf("note ID changed: %d -> %d", firstID, notes[0].ID)
	}
	if !strings.Contains(h.stderr.String(), "updated note 1 on MR !7") {
		t.Errorf("stderr = %q", h.stderr.String())
	}
}

func TestRunFindsMarkerBeyondFirstPage(t *testing.T) {
	h := newHarness(t, fullFixture)
	for i := 0; i < 25; i++ {
		h.fake.Seed("unrelated human comment")
	}
	h.fake.Seed(gccMarker + "\nold report")
	h.fake.SeedSystem("changed the description")

	h.run(t)
	notes := h.fake.Notes()
	if len(notes) != 27 {
		t.Fatalf("notes = %d, want 27 (updated, not duplicated)", len(notes))
	}
	if !strings.Contains(notes[25].Body, "2 active alerts") {
		t.Errorf("marker note not updated: %q", notes[25].Body[:60])
	}
}

func TestRunCIIDSegmentation(t *testing.T) {
	h := newHarness(t, fullFixture)
	h.cfg.CIID = "job-a"
	h.run(t)
	h.cfg.CIID = "job-b"
	h.run(t)
	if n := len(h.fake.Notes()); n != 2 {
		t.Fatalf("notes = %d, want 2 (distinct ci-ids)", n)
	}

	h.cfg.CIID = "job-a"
	h.run(t)
	notes := h.fake.Notes()
	if len(notes) != 2 {
		t.Fatalf("notes = %d after re-run, want 2", len(notes))
	}
	if !strings.Contains(notes[0].Body, `id="job-a"`) || !strings.Contains(notes[1].Body, `id="job-b"`) {
		t.Errorf("segmentation broken: %q / %q", notes[0].Body[:50], notes[1].Body[:50])
	}
}

func TestRunMultiTestbed(t *testing.T) {
	clangPath := clangVariant(t)
	h := newHarness(t, fullFixture)

	h.run(t)
	h.cfg.ReportPath = clangPath
	h.run(t)
	notes := h.fake.Notes()
	if len(notes) != 2 {
		t.Fatalf("notes = %d, want 2 (one per testbed)", len(notes))
	}
	if !strings.Contains(notes[0].Body, gccMarker) || !strings.Contains(notes[0].Body, "[gcc]") {
		t.Errorf("gcc note wrong: %q", notes[0].Body[:80])
	}
	if !strings.Contains(notes[1].Body, clangMarker) || !strings.Contains(notes[1].Body, "[clang]") {
		t.Errorf("clang note wrong: %q", notes[1].Body[:80])
	}

	clangBodyBefore := notes[1].Body
	h.cfg.ReportPath = fullFixture
	h.run(t)
	notes = h.fake.Notes()
	if len(notes) != 2 {
		t.Fatalf("notes = %d after gcc re-run, want 2", len(notes))
	}
	if notes[1].Body != clangBodyBefore {
		t.Error("clang note was touched by the gcc run")
	}
}

func TestZeroAlertModes(t *testing.T) {
	tests := []struct {
		name         string
		mode         ZeroAlertMode
		seed         string
		wantNotes    int
		wantNoWrites bool
	}{
		{name: "post creates green note", mode: ZeroPost, wantNotes: 1},
		{name: "default is post", mode: "", wantNotes: 1},
		{name: "skip makes no API calls", mode: ZeroSkip, wantNotes: 0, wantNoWrites: true},
		{name: "auto without existing note skips", mode: ZeroAuto, wantNotes: 0},
		{name: "auto with existing note updates to green", mode: ZeroAuto, seed: noAlertsMarker + "\n## Bencher Report [clang]: 3 active alerts ⚠️", wantNotes: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, noAlertsFixture)
			h.cfg.OnZeroAlerts = tt.mode
			if tt.seed != "" {
				h.fake.Seed(tt.seed)
			}
			if code := h.run(t); code != ExitOK {
				t.Fatalf("exit = %d, want 0, stderr: %s", code, h.stderr.String())
			}
			notes := h.fake.Notes()
			if len(notes) != tt.wantNotes {
				t.Fatalf("notes = %d, want %d", len(notes), tt.wantNotes)
			}
			if tt.wantNotes == 1 && !strings.Contains(notes[0].Body, "no alerts ✅") {
				t.Errorf("note is not green: %q", notes[0].Body)
			}
			if tt.wantNoWrites && h.fake.Requests.Load() != 0 {
				t.Errorf("requests = %d, want 0", h.fake.Requests.Load())
			}
		})
	}
}

func TestExitCodeAlertsAfterPosting(t *testing.T) {
	h := newHarness(t, fullFixture)
	if code := h.run(t); code != ExitAlerts {
		t.Fatalf("exit = %d, want 1", code)
	}
	if len(h.fake.Notes()) != 1 {
		t.Error("note must be posted before the alert exit")
	}
}

func TestExitCodeNoAlerts(t *testing.T) {
	h := newHarness(t, noAlertsFixture)
	if code := h.run(t); code != ExitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
}

func TestExitCodeParseFailure(t *testing.T) {
	h := newHarness(t, "../report/testdata/invalid.json")
	if code := h.run(t); code != ExitError {
		t.Fatalf("exit = %d, want 2", code)
	}
}

func TestExitCodeMissingConfig(t *testing.T) {
	h := newHarness(t, fullFixture)
	h.cfg.MRIID = ""
	h.cfg.Token = ""
	if code := h.run(t); code != ExitError {
		t.Fatalf("exit = %d, want 2", code)
	}
	msg := h.stderr.String()
	for _, want := range []string{"--mr", "$CI_MERGE_REQUEST_IID", "$BENCHER2GITLAB_TOKEN"} {
		if !strings.Contains(msg, want) {
			t.Errorf("stderr missing %q: %s", want, msg)
		}
	}
}

func TestExitCodeForbiddenDominatesAlerts(t *testing.T) {
	h := newHarness(t, fullFixture)
	h.fake.RejectWrites = true
	if code := h.run(t); code != ExitError {
		t.Fatalf("exit = %d, want 2 (error dominates alert exit)", code)
	}
	if !strings.Contains(h.stderr.String(), "CI_JOB_TOKEN") {
		t.Errorf("stderr missing token hint: %s", h.stderr.String())
	}
}

func TestDryRun(t *testing.T) {
	h := newHarness(t, fullFixture)
	h.cfg.DryRun = true
	h.cfg.Token = ""
	if code := h.run(t); code != ExitAlerts {
		t.Fatalf("exit = %d, want 1 (alerts still signaled)", code)
	}
	if got := h.fake.Requests.Load(); got != 0 {
		t.Errorf("requests = %d, want 0", got)
	}
	out := h.stdout.String()
	if !strings.Contains(out, gccMarker) || !strings.Contains(out, "2 active alerts") {
		t.Errorf("stdout = %q", out)
	}
}

func TestStdinInput(t *testing.T) {
	data, err := os.ReadFile(fullFixture)
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, "-")
	code := Run(context.Background(), h.cfg, bytes.NewReader(data), &h.stdout, &h.stderr)
	if code != ExitAlerts {
		t.Fatalf("exit = %d, stderr: %s", code, h.stderr.String())
	}
	if len(h.fake.Notes()) != 1 {
		t.Errorf("notes = %d, want 1", len(h.fake.Notes()))
	}
}
