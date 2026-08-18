package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/RomanSpies/Bencher2Gitlab/internal/gitlab/gitlabtest"
)

func newFakeClient(t *testing.T, fake *gitlabtest.Fake, token string) *Client {
	t.Helper()
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	return New(srv.URL, token, srv.Client())
}

func TestListMRNotesPagination(t *testing.T) {
	fake := gitlabtest.New()
	for i := 1; i <= 45; i++ {
		fake.Seed(fmt.Sprintf("note %d", i))
	}
	c := newFakeClient(t, fake, "tok")

	notes, err := c.ListMRNotes(context.Background(), "42", "7")
	if err != nil {
		t.Fatalf("ListMRNotes: %v", err)
	}
	if len(notes) != 45 {
		t.Fatalf("got %d notes, want 45", len(notes))
	}
	if notes[0].Body != "note 1" || notes[44].Body != "note 45" {
		t.Errorf("order broken: first=%q last=%q", notes[0].Body, notes[44].Body)
	}
	if got := fake.Requests.Load(); got != 3 {
		t.Errorf("requests = %d, want 3 (pagination)", got)
	}
}

func TestListMRNotesEmpty(t *testing.T) {
	c := newFakeClient(t, gitlabtest.New(), "tok")
	notes, err := c.ListMRNotes(context.Background(), "42", "7")
	if err != nil {
		t.Fatalf("ListMRNotes: %v", err)
	}
	if len(notes) != 0 {
		t.Errorf("got %d notes, want 0", len(notes))
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

func TestJobTokenForbidden(t *testing.T) {
	fake := gitlabtest.New()
	fake.RejectWrites = true
	c := newFakeClient(t, fake, "job-token")

	if _, err := c.ListMRNotes(context.Background(), "42", "7"); err != nil {
		t.Fatalf("ListMRNotes should succeed: %v", err)
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

	_, err := c.ListMRNotes(context.Background(), "42", "7")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("err = %v, want APIError 401", err)
	}
}

func TestContextCancellation(t *testing.T) {
	c := newFakeClient(t, gitlabtest.New(), "tok")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.ListMRNotes(ctx, "42", "7"); !errors.Is(err, context.Canceled) {
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
			_, err := c.ListMRNotes(context.Background(), "1", "1")
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
