package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/RomanSpies/Bencher2Gitlab/internal/app"
)

func main() {
	cfg, err := parseArgs(os.Args[1:])
	if err != nil {
		if err == flag.ErrHelp {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "bencher2gitlab:", err)
		os.Exit(app.ExitError)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(app.Run(ctx, cfg, os.Stdin, os.Stdout, os.Stderr))
}

func parseArgs(args []string) (app.Config, error) {
	fs := flag.NewFlagSet("bencher2gitlab", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `Usage: bencher run --quiet --format json ... | bencher2gitlab [flags]

Posts a Bencher report as an idempotent GitLab merge request note.
Exits 0 (no active alerts), 1 (active alerts, note posted first), 2 (error).

Token: $BENCHER2GITLAB_TOKEN (preferred) or $GITLAB_TOKEN — an access token
with 'api' scope. CI_JOB_TOKEN does NOT work (403 on note create/update).

Flags:
`)
		fs.PrintDefaults()
	}

	var cfg app.Config
	var onZero string
	fs.StringVar(&cfg.ReportPath, "report", "-", "bencher JsonReport file, \"-\" for stdin")
	fs.StringVar(&cfg.GitLabURL, "gitlab-url", os.Getenv("CI_API_V4_URL"), "GitLab API v4 base URL (default $CI_API_V4_URL)")
	fs.StringVar(&cfg.ProjectID, "project", os.Getenv("CI_PROJECT_ID"), "GitLab project ID or path (default $CI_PROJECT_ID)")
	fs.StringVar(&cfg.MRIID, "mr", os.Getenv("CI_MERGE_REQUEST_IID"), "merge request IID (default $CI_MERGE_REQUEST_IID)")
	fs.StringVar(&cfg.CIID, "ci-id", "", "idempotency segment for multiple bencher jobs per MR (default: project/branch/testbed/adapter)")
	fs.StringVar(&cfg.BencherURL, "bencher-url", "https://bencher.dev", "Bencher console base URL for the report link")
	fs.StringVar(&onZero, "on-zero-alerts", "post", "behavior without active alerts: post | auto | skip")
	fs.BoolVar(&cfg.DryRun, "dry-run", false, "print the markdown to stdout, no API calls")
	fs.DurationVar(&cfg.Timeout, "timeout", 30*time.Second, "total timeout for GitLab API calls")
	if err := fs.Parse(args); err != nil {
		return app.Config{}, err
	}
	if fs.NArg() > 0 {
		return app.Config{}, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}

	switch app.ZeroAlertMode(onZero) {
	case app.ZeroPost, app.ZeroAuto, app.ZeroSkip:
		cfg.OnZeroAlerts = app.ZeroAlertMode(onZero)
	default:
		return app.Config{}, fmt.Errorf("invalid --on-zero-alerts %q (want post, auto, or skip)", onZero)
	}

	cfg.Token = os.Getenv("BENCHER2GITLAB_TOKEN")
	if cfg.Token == "" {
		cfg.Token = os.Getenv("GITLAB_TOKEN")
	}
	return cfg, nil
}
