package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/RomanSpies/Bencher2Gitlab/internal/gitlab/gitlabtest"
)

func newFakeClient(t *testing.T, fake *gitlabtest.Fake, token string) *Client {
	t.Helper()
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	return New(srv.URL, token, srv.Client())
}

func recordSleeps(c *Client) *[]time.Duration {
	var slept []time.Duration
	c.retry.sleep = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}
	return &slept
}

func TestListMRDiscussionsPagination(t *testing.T) {
	fake := gitlabtest.New()
	for i := 1; i <= 45; i++ {
		fake.Seed(fmt.Sprintf("note %d", i))
	}
	c := newFakeClient(t, fake, "tok")

	discussions, err := c.ListMRDiscussions(context.Background(), "42", "7")
	if err != nil {
		t.Fatalf("ListMRDiscussions: %v", err)
	}
	if len(discussions) != 45 {
		t.Fatalf("got %d discussions, want 45", len(discussions))
	}
	if discussions[0].Notes[0].Body != "note 1" || discussions[44].Notes[0].Body != "note 45" {
		t.Errorf("order broken: first=%q last=%q", discussions[0].Notes[0].Body, discussions[44].Notes[0].Body)
	}
	if !discussions[0].IndividualNote || discussions[0].Notes[0].Author.ID != gitlabtest.BotUserID {
		t.Errorf("discussion fields not decoded: %+v", discussions[0])
	}
	if got := fake.Requests.Load(); got != 3 {
		t.Errorf("requests = %d, want 3 (pagination)", got)
	}
}

func TestListMRDiscussionsEmpty(t *testing.T) {
	c := newFakeClient(t, gitlabtest.New(), "tok")
	discussions, err := c.ListMRDiscussions(context.Background(), "42", "7")
	if err != nil {
		t.Fatalf("ListMRDiscussions: %v", err)
	}
	if len(discussions) != 0 {
		t.Errorf("got %d discussions, want 0", len(discussions))
	}
}

func TestCurrentUser(t *testing.T) {
	c := newFakeClient(t, gitlabtest.New(), "tok")
	u, err := c.CurrentUser(context.Background())
	if err != nil {
		t.Fatalf("CurrentUser: %v", err)
	}
	if u.ID != gitlabtest.BotUserID {
		t.Errorf("user = %+v, want ID %d", u, gitlabtest.BotUserID)
	}
}

func TestCurrentUserUnauthorizedCarriesHint(t *testing.T) {
	fake := gitlabtest.New()
	fake.RequireToken = "right"
	c := newFakeClient(t, fake, "job-token")
	_, err := c.CurrentUser(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusUnauthorized || !strings.Contains(err.Error(), "CI_JOB_TOKEN") {
		t.Fatalf("err = %v, want APIError 401 with token hint", err)
	}
}

func TestCreateNote(t *testing.T) {
	fake := gitlabtest.New()
	var gotPath, gotToken, gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		gotToken = r.Header.Get("PRIVATE-TOKEN")
		gotContentType = r.Header.Get("Content-Type")
		fake.Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	c := New(srv.URL+"/", "test-token", srv.Client())

	note, err := c.CreateMRNote(context.Background(), "group/proj", "7", "hello **md**")
	if err != nil {
		t.Fatalf("CreateMRNote: %v", err)
	}
	if want := "/projects/group%2Fproj/merge_requests/7/notes"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if gotToken != "test-token" {
		t.Errorf("PRIVATE-TOKEN = %q", gotToken)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q", gotContentType)
	}
	if note.ID == 0 || note.Body != "hello **md**" {
		t.Errorf("note = %+v", note)
	}
	if stored := fake.Notes(); len(stored) != 1 || stored[0].Body != "hello **md**" {
		t.Errorf("stored = %+v", stored)
	}
}

func TestUpdateNote(t *testing.T) {
	fake := gitlabtest.New()
	fake.Seed("old body")
	c := newFakeClient(t, fake, "tok")

	note, err := c.UpdateMRNote(context.Background(), "42", "7", 1, "new body")
	if err != nil {
		t.Fatalf("UpdateMRNote: %v", err)
	}
	if note.ID != 1 || note.Body != "new body" {
		t.Errorf("note = %+v", note)
	}

	_, err = c.UpdateMRNote(context.Background(), "42", "7", 999, "x")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
		t.Fatalf("err = %v, want APIError 404", err)
	}
}

func TestThreadLifecycle(t *testing.T) {
	fake := gitlabtest.New()
	c := newFakeClient(t, fake, "tok")
	ctx := context.Background()

	d, err := c.CreateMRDiscussion(ctx, "42", "7", "thread body")
	if err != nil {
		t.Fatalf("CreateMRDiscussion: %v", err)
	}
	if d.IndividualNote || len(d.Notes) != 1 || !d.Notes[0].Resolvable || d.Resolved() {
		t.Fatalf("new thread must be one unresolved resolvable note: %+v", d)
	}

	if _, err := c.UpdateMRDiscussionNote(ctx, "42", "7", d.ID, d.Notes[0].ID, "edited"); err != nil {
		t.Fatalf("UpdateMRDiscussionNote: %v", err)
	}
	resolved, err := c.ResolveMRDiscussion(ctx, "42", "7", d.ID, true)
	if err != nil {
		t.Fatalf("ResolveMRDiscussion: %v", err)
	}
	if !resolved.Resolved() || resolved.Notes[0].Body != "edited" {
		t.Errorf("after resolve: %+v", resolved)
	}
	reopened, err := c.ResolveMRDiscussion(ctx, "42", "7", d.ID, false)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if reopened.Resolved() {
		t.Errorf("thread still resolved after reopen: %+v", reopened)
	}
}

func TestDiscussionResolved(t *testing.T) {
	n := func(resolvable, resolved bool) Note { return Note{Resolvable: resolvable, Resolved: resolved} }
	tests := []struct {
		name  string
		notes []Note
		want  bool
	}{
		{"no notes", nil, false},
		{"plain comment", []Note{n(false, false)}, false},
		{"open thread", []Note{n(true, false)}, false},
		{"resolved thread", []Note{n(true, true)}, true},
		{"resolved with non-resolvable reply", []Note{n(true, true), n(false, false)}, true},
		{"one open reply", []Note{n(true, true), n(true, false)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Discussion{Notes: tt.notes}
			if got := d.Resolved(); got != tt.want {
				t.Errorf("Resolved() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestJobTokenForbidden(t *testing.T) {
	fake := gitlabtest.New()
	fake.RejectWrites = true
	c := newFakeClient(t, fake, "job-token")

	if _, err := c.ListMRDiscussions(context.Background(), "42", "7"); err != nil {
		t.Fatalf("ListMRDiscussions should succeed: %v", err)
	}

	_, err := c.CreateMRNote(context.Background(), "42", "7", "x")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusForbidden {
		t.Fatalf("err = %v, want APIError 403", err)
	}
	if !strings.Contains(err.Error(), "CI_JOB_TOKEN") {
		t.Errorf("error must carry the token hint, got: %v", err)
	}

	_, err = c.UpdateMRNote(context.Background(), "42", "7", 1, "x")
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusForbidden || !strings.Contains(err.Error(), "CI_JOB_TOKEN") {
		t.Errorf("update err = %v, want APIError 403 with hint", err)
	}
}

func TestUnauthorized(t *testing.T) {
	fake := gitlabtest.New()
	fake.RequireToken = "right"
	c := newFakeClient(t, fake, "wrong")

	_, err := c.ListMRDiscussions(context.Background(), "42", "7")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("err = %v, want APIError 401", err)
	}
}

func TestContextCancellation(t *testing.T) {
	c := newFakeClient(t, gitlabtest.New(), "tok")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.ListMRDiscussions(ctx, "42", "7"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestErrorBodyParsing(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantMsg string
	}{
		{"message string", 403, `{"message":"403 Forbidden"}`, "403 Forbidden"},
		{"error field", 400, `{"error":"bad request"}`, "bad request"},
		{"non-json", 502, "Bad Gateway HTML", "Bad Gateway HTML"},
		{"empty body", 500, "", "Internal Server Error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			}))
			t.Cleanup(srv.Close)
			c := New(srv.URL, "tok", srv.Client())
			c.retry.attempts = 1
			_, err := c.ListMRDiscussions(context.Background(), "1", "1")
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("err = %v, want APIError", err)
			}
			if apiErr.StatusCode != tt.status || !strings.Contains(apiErr.Message, tt.wantMsg) {
				t.Errorf("got %d %q, want %d containing %q", apiErr.StatusCode, apiErr.Message, tt.status, tt.wantMsg)
			}
		})
	}
}

func TestRetryIdempotentOnServerErrors(t *testing.T) {
	fake := gitlabtest.New()
	fake.Seed("note")
	fake.FailNext(gitlabtest.Fault{Status: 503}, gitlabtest.Fault{Status: 502})
	c := newFakeClient(t, fake, "tok")
	slept := recordSleeps(c)

	discussions, err := c.ListMRDiscussions(context.Background(), "42", "7")
	if err != nil {
		t.Fatalf("ListMRDiscussions should succeed after transient 5xx: %v", err)
	}
	if len(discussions) != 1 {
		t.Errorf("discussions = %d, want 1", len(discussions))
	}
	if want := []time.Duration{500 * time.Millisecond, time.Second}; !slices.Equal(*slept, want) {
		t.Errorf("backoff = %v, want exponential %v", *slept, want)
	}
}

func TestRetryHonorsRetryAfter(t *testing.T) {
	fake := gitlabtest.New()
	fake.FailNext(gitlabtest.Fault{Status: 429, RetryAfter: "3"})
	c := newFakeClient(t, fake, "tok")
	slept := recordSleeps(c)

	if _, err := c.CurrentUser(context.Background()); err != nil {
		t.Fatalf("CurrentUser should succeed after 429: %v", err)
	}
	if want := []time.Duration{3 * time.Second}; !slices.Equal(*slept, want) {
		t.Errorf("slept %v, want Retry-After %v", *slept, want)
	}
}

func TestRetryPostOnlyOnRateLimit(t *testing.T) {
	t.Run("429 is retried because the request never reached the application", func(t *testing.T) {
		fake := gitlabtest.New()
		fake.FailNext(gitlabtest.Fault{Status: 429})
		c := newFakeClient(t, fake, "tok")
		recordSleeps(c)
		if _, err := c.CreateMRNote(context.Background(), "42", "7", "x"); err != nil {
			t.Fatalf("CreateMRNote: %v", err)
		}
		if n := len(fake.Notes()); n != 1 {
			t.Errorf("notes = %d, want 1", n)
		}
	})
	t.Run("503 is not retried because the note may already exist", func(t *testing.T) {
		fake := gitlabtest.New()
		fake.FailNext(gitlabtest.Fault{Status: 503})
		c := newFakeClient(t, fake, "tok")
		slept := recordSleeps(c)
		_, err := c.CreateMRDiscussion(context.Background(), "42", "7", "x")
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("err = %v, want APIError 503", err)
		}
		if len(*slept) != 0 || fake.Requests.Load() != 1 {
			t.Errorf("POST was retried: sleeps=%v requests=%d", *slept, fake.Requests.Load())
		}
	})
}

func TestRetryGivesUpAfterAttempts(t *testing.T) {
	fake := gitlabtest.New()
	fake.FailNext(gitlabtest.Fault{Status: 500}, gitlabtest.Fault{Status: 500}, gitlabtest.Fault{Status: 500}, gitlabtest.Fault{Status: 500})
	c := newFakeClient(t, fake, "tok")
	recordSleeps(c)

	_, err := c.UpdateMRNote(context.Background(), "42", "7", 1, "x")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("err = %v, want final APIError 500", err)
	}
	if got := fake.Requests.Load(); got != int64(defaultRetry.attempts) {
		t.Errorf("requests = %d, want %d", got, defaultRetry.attempts)
	}
}

func TestRetryNotOnClientErrors(t *testing.T) {
	fake := gitlabtest.New()
	fake.FailNext(gitlabtest.Fault{Status: 404})
	c := newFakeClient(t, fake, "tok")
	recordSleeps(c)
	if _, err := c.ListMRDiscussions(context.Background(), "42", "7"); err == nil {
		t.Fatal("want 404 error")
	}
	if got := fake.Requests.Load(); got != 1 {
		t.Errorf("requests = %d, want 1 (4xx other than 429 is final)", got)
	}
}

func TestRetryAfterBeyondDeadlineFailsFast(t *testing.T) {
	fake := gitlabtest.New()
	fake.FailNext(gitlabtest.Fault{Status: 429, RetryAfter: "120"})
	c := newFakeClient(t, fake, "tok")
	slept := recordSleeps(c)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := c.CurrentUser(ctx)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("err = %v, want APIError 429", err)
	}
	if len(*slept) != 0 {
		t.Errorf("slept %v although Retry-After exceeds the deadline", *slept)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		in     string
		want   time.Duration
		wantOK bool
	}{
		{"", 0, false},
		{"7", 7 * time.Second, true},
		{"-1", 0, false},
		{"soon", 0, false},
		{now.Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second, true},
		{now.Add(-time.Minute).Format(http.TimeFormat), 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, ok := parseRetryAfter(tt.in, now)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("parseRetryAfter(%q) = %v, %t; want %v, %t", tt.in, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestBackoffCapped(t *testing.T) {
	if got := defaultRetry.backoff(10, nil); got != defaultRetry.max {
		t.Errorf("backoff(10) = %v, want cap %v", got, defaultRetry.max)
	}
}

func TestOversizedResponseIsExplicitError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		chunk := []byte(strings.Repeat(" ", 1<<20))
		for range maxResponseBytes>>20 + 1 {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	c := New(srv.URL, "tok", srv.Client())

	_, err := c.ListMRDiscussions(context.Background(), "42", "7")
	if err == nil || !strings.Contains(err.Error(), "exceeds 16 MiB") {
		t.Fatalf("err = %v, want explicit size error instead of a JSON decode failure", err)
	}
}
