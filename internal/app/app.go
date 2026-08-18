package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/RomanSpies/Bencher2Gitlab/internal/gitlab"
	"github.com/RomanSpies/Bencher2Gitlab/internal/markdown"
	"github.com/RomanSpies/Bencher2Gitlab/internal/report"
)

type ZeroAlertMode string

const (
	ZeroPost ZeroAlertMode = "post"
	ZeroAuto ZeroAlertMode = "auto"
	ZeroSkip ZeroAlertMode = "skip"
)

const (
	ExitOK     = 0
	ExitAlerts = 1
	ExitError  = 2
)

const logPrefix = "bencher2gitlab:"

type Config struct {
	ReportPath   string
	GitLabURL    string
	ProjectID    string
	MRIID        string
	Token        string
	CIID         string
	BencherURL   string
	OnZeroAlerts ZeroAlertMode
	DryRun       bool
	Timeout      time.Duration
	HTTPClient   *http.Client
}

func Run(ctx context.Context, cfg Config, in io.Reader, stdout, stderr io.Writer) int {
	if cfg.OnZeroAlerts == "" {
		cfg.OnZeroAlerts = ZeroPost
	}
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}

	rep, err := loadReport(cfg.ReportPath, in)
	if err != nil {
		return fail(stderr, err)
	}

	marker := markerFor(rep, cfg.CIID)
	body := markdown.Render(rep, markdown.Options{Marker: marker, BencherURL: cfg.BencherURL})
	activeAlerts := len(rep.ActiveAlerts())
	exit := ExitOK
	if activeAlerts > 0 {
		exit = ExitAlerts
	}

	if cfg.DryRun {
		fmt.Fprintln(stdout, body)
		return exit
	}
	if err := validate(cfg); err != nil {
		return fail(stderr, err)
	}
	if err := publish(ctx, cfg, marker, body, activeAlerts, stderr); err != nil {
		return fail(stderr, err)
	}
	if exit == ExitAlerts {
		fmt.Fprintf(stderr, "%s %d active alert(s) — failing the job\n", logPrefix, activeAlerts)
	}
	return exit
}

func publish(ctx context.Context, cfg Config, marker, body string, activeAlerts int, stderr io.Writer) error {
	if activeAlerts == 0 && cfg.OnZeroAlerts == ZeroSkip {
		fmt.Fprintln(stderr, logPrefix, "no active alerts, skipping (--on-zero-alerts=skip)")
		return nil
	}
	client := gitlab.New(cfg.GitLabURL, cfg.Token, cfg.HTTPClient)
	notes, err := client.ListMRNotes(ctx, cfg.ProjectID, cfg.MRIID)
	if err != nil {
		return err
	}
	existing := findMarkerNote(notes, marker)
	if activeAlerts == 0 && cfg.OnZeroAlerts == ZeroAuto && existing == nil {
		fmt.Fprintln(stderr, logPrefix, "no active alerts and no existing note, skipping (--on-zero-alerts=auto)")
		return nil
	}
	if existing != nil {
		if _, err := client.UpdateMRNote(ctx, cfg.ProjectID, cfg.MRIID, existing.ID, body); err != nil {
			return err
		}
		fmt.Fprintf(stderr, "%s updated note %d on MR !%s\n", logPrefix, existing.ID, cfg.MRIID)
		return nil
	}
	note, err := client.CreateMRNote(ctx, cfg.ProjectID, cfg.MRIID, body)
	if err != nil {
		return err
	}
	fmt.Fprintf(stderr, "%s created note %d on MR !%s\n", logPrefix, note.ID, cfg.MRIID)
	return nil
}

func fail(stderr io.Writer, err error) int {
	fmt.Fprintln(stderr, logPrefix, err)
	return ExitError
}

func loadReport(path string, in io.Reader) (*report.Report, error) {
	if path == "" || path == "-" {
		return report.Parse(in)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening report: %w", err)
	}
	defer f.Close()
	return report.Parse(f)
}

func validate(cfg Config) error {
	var missing []string
	if cfg.GitLabURL == "" {
		missing = append(missing, "--gitlab-url (or $CI_API_V4_URL)")
	}
	if cfg.ProjectID == "" {
		missing = append(missing, "--project (or $CI_PROJECT_ID)")
	}
	if cfg.MRIID == "" {
		missing = append(missing, "--mr (or $CI_MERGE_REQUEST_IID)")
	}
	if cfg.Token == "" {
		missing = append(missing, "a token via $BENCHER2GITLAB_TOKEN or $GITLAB_TOKEN")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing configuration: %s", strings.Join(missing, ", "))
	}
	return nil
}

func markerFor(rep *report.Report, ciID string) string {
	id := ciID
	if id == "" {
		id = rep.Project.Slug + "/" + rep.Branch.Slug + "/" + rep.Testbed.Slug + "/" + rep.Adapter
	}
	return fmt.Sprintf(`<!-- bencher2gitlab id="%s" -->`, sanitizeMarkerID(id))
}

func sanitizeMarkerID(s string) string {
	r := strings.NewReplacer("-->", "", `"`, "", "\n", "", "\r", "")
	return r.Replace(s)
}

func findMarkerNote(notes []gitlab.Note, marker string) *gitlab.Note {
	for i := range notes {
		if !notes[i].System && strings.Contains(notes[i].Body, marker) {
			return &notes[i]
		}
	}
	return nil
}
