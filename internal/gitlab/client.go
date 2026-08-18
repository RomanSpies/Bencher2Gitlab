package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const tokenHint = "CI_JOB_TOKEN cannot create or update notes (gitlab-org/gitlab#464591); " +
	"supply a project/group/personal access token with `api` scope via BENCHER2GITLAB_TOKEN or GITLAB_TOKEN"

const maxPages = 100

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func New(baseURL, token string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{baseURL: strings.TrimSuffix(baseURL, "/"), token: token, http: hc}
}

type Note struct {
	ID     int64  `json:"id"`
	Body   string `json:"body"`
	System bool   `json:"system"`
}

type APIError struct {
	StatusCode int
	Message    string
	Hint       string
}

func (e *APIError) Error() string {
	s := fmt.Sprintf("gitlab api: HTTP %d: %s", e.StatusCode, e.Message)
	if e.Hint != "" {
		s += " (hint: " + e.Hint + ")"
	}
	return s
}

func (c *Client) notesURL(project, mrIID string) string {
	return fmt.Sprintf("%s/projects/%s/merge_requests/%s/notes",
		c.baseURL, url.PathEscape(project), url.PathEscape(mrIID))
}

func (c *Client) ListMRNotes(ctx context.Context, project, mrIID string) ([]Note, error) {
	var all []Note
	next := "1"
	for page := 0; page < maxPages; page++ {
		u := fmt.Sprintf("%s?per_page=100&page=%s", c.notesURL(project, mrIID), next)
		var notes []Note
		hdr, err := c.do(ctx, http.MethodGet, u, nil, &notes)
		if err != nil {
			return nil, fmt.Errorf("listing MR notes: %w", err)
		}
		all = append(all, notes...)
		next = hdr.Get("x-next-page")
		if next == "" {
			return all, nil
		}
	}
	return nil, fmt.Errorf("listing MR notes: more than %d pages, giving up", maxPages)
}

func (c *Client) CreateMRNote(ctx context.Context, project, mrIID, body string) (*Note, error) {
	var note Note
	_, err := c.do(ctx, http.MethodPost, c.notesURL(project, mrIID), map[string]string{"body": body}, &note)
	if err != nil {
		return nil, fmt.Errorf("creating MR note: %w", withTokenHint(err))
	}
	return &note, nil
}

func (c *Client) UpdateMRNote(ctx context.Context, project, mrIID string, noteID int64, body string) (*Note, error) {
	u := fmt.Sprintf("%s/%d", c.notesURL(project, mrIID), noteID)
	var note Note
	_, err := c.do(ctx, http.MethodPut, u, map[string]string{"body": body}, &note)
	if err != nil {
		return nil, fmt.Errorf("updating MR note %d: %w", noteID, withTokenHint(err))
	}
	return &note, nil
}

func withTokenHint(err error) error {
	var apiErr *APIError
	if errors.As(err, &apiErr) && (apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden) {
		apiErr.Hint = tokenHint
	}
	return err
}

func (c *Client) do(ctx context.Context, method, u string, body, out any) (http.Header, error) {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encoding request: %w", err)
		}
		reqBody = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reqBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, &APIError{StatusCode: resp.StatusCode, Message: apiMessage(data, resp.StatusCode)}
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return nil, fmt.Errorf("decoding response: %w", err)
		}
	}
	return resp.Header, nil
}

func apiMessage(body []byte, status int) string {
	var e struct {
		Message any    `json:"message"`
		Err     string `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil {
		if e.Message != nil {
			return fmt.Sprintf("%v", e.Message)
		}
		if e.Err != "" {
			return e.Err
		}
	}
	if s := strings.TrimSpace(string(body)); s != "" && len(s) <= 200 {
		return s
	}
	return http.StatusText(status)
}
