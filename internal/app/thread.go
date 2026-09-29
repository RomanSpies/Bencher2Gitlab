package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/RomanSpies/Bencher2Gitlab/internal/gitlab"
	"github.com/RomanSpies/Bencher2Gitlab/internal/report"
)

var alertSetPattern = regexp.MustCompile(`<!-- bencher2gitlab alerts="([0-9a-f ]*)" -->`)

type threadState struct {
	Resolved bool
	Alerts   []string
}

func (s threadState) marker() string {
	return fmt.Sprintf(`<!-- bencher2gitlab alerts="%s" -->`, strings.Join(s.Alerts, " "))
}

func nextThreadState(prev *threadState, current []string) threadState {
	if prev != nil && prev.Resolved && isSubset(current, prev.Alerts) {
		return *prev
	}
	return threadState{Resolved: len(current) == 0, Alerts: current}
}

func alertFingerprints(rep *report.Report) []string {
	seen := make(map[string]struct{})
	for _, a := range rep.ActiveAlerts() {
		sum := sha256.Sum256([]byte(a.Benchmark.Slug + "\x00" + a.Threshold.Measure.Slug + "\x00" + a.Limit))
		seen[hex.EncodeToString(sum[:6])] = struct{}{}
	}
	return slices.Sorted(maps.Keys(seen))
}

func parseAlertSet(body string) []string {
	m := alertSetPattern.FindStringSubmatch(body)
	if m == nil {
		return nil
	}
	return slices.Sorted(slices.Values(strings.Fields(m[1])))
}

func isSubset(sub, super []string) bool {
	for _, s := range sub {
		if _, found := slices.BinarySearch(super, s); !found {
			return false
		}
	}
	return true
}

func (p publisher) thread(ctx context.Context, rep *report.Report, marker string, existing *gitlab.Discussion) error {
	current := alertFingerprints(rep)
	if existing == nil {
		return p.createThread(ctx, rep, marker, nextThreadState(nil, current))
	}
	if existing.IndividualNote {
		fmt.Fprintf(p.stderr, "%s note %d is a plain comment from an earlier run without --thread and cannot be resolved; "+
			"updating it in place (delete it to get a thread)\n", logPrefix, existing.Notes[0].ID)
		return p.updateBody(ctx, existing, render(rep, p.cfg, marker, nil))
	}

	prev := threadState{Resolved: existing.Resolved(), Alerts: parseAlertSet(existing.Notes[0].Body)}
	next := nextThreadState(&prev, current)
	if err := p.updateBody(ctx, existing, render(rep, p.cfg, marker, &next)); err != nil {
		return err
	}
	if next.Resolved == prev.Resolved {
		if next.Resolved && len(current) > 0 {
			fmt.Fprintf(p.stderr, "%s thread %s stays resolved: all %d active alert(s) were already acknowledged\n",
				logPrefix, existing.ID, len(current))
		}
		return nil
	}
	return p.setResolved(ctx, existing.ID, next.Resolved)
}

func (p publisher) createThread(ctx context.Context, rep *report.Report, marker string, state threadState) error {
	d, err := p.client.CreateMRDiscussion(ctx, p.cfg.ProjectID, p.cfg.MRIID, render(rep, p.cfg, marker, &state))
	if err != nil {
		return err
	}
	fmt.Fprintf(p.stderr, "%s created thread %s on MR !%s\n", logPrefix, d.ID, p.cfg.MRIID)
	if !state.Resolved {
		return nil
	}
	return p.setResolved(ctx, d.ID, true)
}

func (p publisher) setResolved(ctx context.Context, discussionID string, resolved bool) error {
	if _, err := p.client.ResolveMRDiscussion(ctx, p.cfg.ProjectID, p.cfg.MRIID, discussionID, resolved); err != nil {
		return err
	}
	verb := "reopened"
	if resolved {
		verb = "resolved"
	}
	fmt.Fprintf(p.stderr, "%s %s thread %s on MR !%s\n", logPrefix, verb, discussionID, p.cfg.MRIID)
	return nil
}
