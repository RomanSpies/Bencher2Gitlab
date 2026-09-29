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
	Thread       bool
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
	activeAlerts := len(rep.ActiveAlerts())
	exit := ExitOK
	if activeAlerts > 0 {
		exit = ExitAlerts
	}

	if cfg.DryRun {
		var state *threadState
		if cfg.Thread {
			s := nextThreadState(nil, alertFingerprints(rep))
			state = &s
		}
		fmt.Fprintln(stdout, render(rep, cfg, marker, state))
		return exit
	}
	if err := validate(cfg); err != nil {
		return fail(stderr, err)
	}
	if err := publish(ctx, cfg, rep, marker, stderr); err != nil {
		return fail(stderr, err)
	}
	if exit == ExitAlerts {
		fmt.Fprintf(stderr, "%s %d active alert(s) — failing the job\n", logPrefix, activeAlerts)
	}
	return exit
}

type publisher struct {
	client *gitlab.Client
	cfg    Config
	stderr io.Writer
}

func publish(ctx context.Context, cfg Config, rep *report.Report, marker string, stderr io.Writer) error {
	activeAlerts := len(rep.ActiveAlerts())
	if activeAlerts == 0 && cfg.OnZeroAlerts == ZeroSkip {
		fmt.Fprintln(stderr, logPrefix, "no active alerts, skipping (--on-zero-alerts=skip)")
		return nil
	}
	client := gitlab.New(cfg.GitLabURL, cfg.Token, cfg.HTTPClient)
	self, err := client.CurrentUser(ctx)
	if err != nil {
		return err
	}
	discussions, err := client.ListMRDiscussions(ctx, cfg.ProjectID, cfg.MRIID)
	if err != nil {
		return err
	}
	existing := findOwnDiscussion(discussions, marker, self.ID)
	if activeAlerts == 0 && cfg.OnZeroAlerts == ZeroAuto && existing == nil {
		fmt.Fprintln(stderr, logPrefix, "no active alerts and no existing note, skipping (--on-zero-alerts=auto)")
		return nil
	}
	p := publisher{client: client, cfg: cfg, stderr: stderr}
	if cfg.Thread {
		return p.thread(ctx, rep, marker, existing)
	}
	return p.note(ctx, rep, marker, existing)
}

func (p publisher) note(ctx context.Context, rep *report.Report, marker string, existing *gitlab.Discussion) error {
	body := render(rep, p.cfg, marker, nil)
	if existing != nil {
		return p.updateBody(ctx, existing, body)
	}
	note, err := p.client.CreateMRNote(ctx, p.cfg.ProjectID, p.cfg.MRIID, body)
	if err != nil {
		return err
	}
	fmt.Fprintf(p.stderr, "%s created note %d on MR !%s\n", logPrefix, note.ID, p.cfg.MRIID)
	return nil
}

func (p publisher) updateBody(ctx context.Context, d *gitlab.Discussion, body string) error {
	note := d.Notes[0]
	var err error
	if d.IndividualNote {
		_, err = p.client.UpdateMRNote(ctx, p.cfg.ProjectID, p.cfg.MRIID, note.ID, body)
	} else {
		_, err = p.client.UpdateMRDiscussionNote(ctx, p.cfg.ProjectID, p.cfg.MRIID, d.ID, note.ID, body)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(p.stderr, "%s updated note %d on MR !%s\n", logPrefix, note.ID, p.cfg.MRIID)
	return nil
}

func render(rep *report.Report, cfg Config, marker string, state *threadState) string {
	header := marker
	if state != nil {
		header += "\n" + state.marker()
	}
	return markdown.Render(rep, markdown.Options{Marker: header, BencherURL: cfg.BencherURL})
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

func findOwnDiscussion(discussions []gitlab.Discussion, marker string, self int64) *gitlab.Discussion {
	for i := range discussions {
		if len(discussions[i].Notes) == 0 {
			continue
		}
		first := discussions[i].Notes[0]
		if !first.System && first.Author.ID == self && strings.Contains(first.Body, marker) {
			return &discussions[i]
		}
	}
	return nil
}
