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
	"strconv"
	"strings"
	"time"
)

const tokenHint = "CI_JOB_TOKEN cannot create or update notes (gitlab-org/gitlab#464591); " +
	"supply a project/group/personal access token with `api` scope via BENCHER2GITLAB_TOKEN or GITLAB_TOKEN"

const maxPages = 100

const maxResponseBytes = 16 << 20

type retryPolicy struct {
	attempts int
	base     time.Duration
	max      time.Duration
	sleep    func(context.Context, time.Duration) error
}

var defaultRetry = retryPolicy{
	attempts: 4,
	base:     500 * time.Millisecond,
	max:      8 * time.Second,
	sleep:    sleepContext,
}

type Client struct {
	baseURL string
	token   string
	http    *http.Client
	retry   retryPolicy
}

func New(baseURL, token string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{baseURL: strings.TrimSuffix(baseURL, "/"), token: token, http: hc, retry: defaultRetry}
}

type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

type Note struct {
	ID         int64  `json:"id"`
	Body       string `json:"body"`
	System     bool   `json:"system"`
	Author     User   `json:"author"`
	Resolvable bool   `json:"resolvable"`
	Resolved   bool   `json:"resolved"`
}

type Discussion struct {
	ID             string `json:"id"`
	IndividualNote bool   `json:"individual_note"`
	Notes          []Note `json:"notes"`
}

func (d *Discussion) Resolved() bool {
	resolvable := false
	for _, n := range d.Notes {
		if !n.Resolvable {
			continue
		}
		if !n.Resolved {
			return false
		}
		resolvable = true
	}
	return resolvable
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

func (c *Client) mrURL(project, mrIID, collection string) string {
	return fmt.Sprintf("%s/projects/%s/merge_requests/%s/%s",
		c.baseURL, url.PathEscape(project), url.PathEscape(mrIID), collection)
}

func (c *Client) CurrentUser(ctx context.Context) (*User, error) {
	var u User
	if _, err := c.do(ctx, http.MethodGet, c.baseURL+"/user", nil, &u); err != nil {
		return nil, fmt.Errorf("resolving token user: %w", withTokenHint(err))
	}
	return &u, nil
}

func (c *Client) ListMRDiscussions(ctx context.Context, project, mrIID string) ([]Discussion, error) {
	var all []Discussion
	next := "1"
	for page := 0; page < maxPages; page++ {
		u := fmt.Sprintf("%s?per_page=100&page=%s", c.mrURL(project, mrIID, "discussions"), next)
		var discussions []Discussion
		hdr, err := c.do(ctx, http.MethodGet, u, nil, &discussions)
		if err != nil {
			return nil, fmt.Errorf("listing MR discussions: %w", err)
		}
		all = append(all, discussions...)
		next = hdr.Get("x-next-page")
		if next == "" {
			return all, nil
		}
	}
	return nil, fmt.Errorf("listing MR discussions: more than %d pages, giving up", maxPages)
}

func (c *Client) CreateMRNote(ctx context.Context, project, mrIID, body string) (*Note, error) {
	var note Note
	_, err := c.do(ctx, http.MethodPost, c.mrURL(project, mrIID, "notes"), map[string]string{"body": body}, &note)
	if err != nil {
		return nil, fmt.Errorf("creating MR note: %w", withTokenHint(err))
	}
	return &note, nil
}

func (c *Client) UpdateMRNote(ctx context.Context, project, mrIID string, noteID int64, body string) (*Note, error) {
	u := fmt.Sprintf("%s/%d", c.mrURL(project, mrIID, "notes"), noteID)
	var note Note
	_, err := c.do(ctx, http.MethodPut, u, map[string]string{"body": body}, &note)
	if err != nil {
		return nil, fmt.Errorf("updating MR note %d: %w", noteID, withTokenHint(err))
	}
	return &note, nil
}

func (c *Client) CreateMRDiscussion(ctx context.Context, project, mrIID, body string) (*Discussion, error) {
	var d Discussion
	_, err := c.do(ctx, http.MethodPost, c.mrURL(project, mrIID, "discussions"), map[string]string{"body": body}, &d)
	if err != nil {
		return nil, fmt.Errorf("creating MR thread: %w", withTokenHint(err))
	}
	return &d, nil
}

func (c *Client) UpdateMRDiscussionNote(ctx context.Context, project, mrIID, discussionID string, noteID int64, body string) (*Note, error) {
	u := fmt.Sprintf("%s/%s/notes/%d", c.mrURL(project, mrIID, "discussions"), url.PathEscape(discussionID), noteID)
	var note Note
	_, err := c.do(ctx, http.MethodPut, u, map[string]string{"body": body}, &note)
	if err != nil {
		return nil, fmt.Errorf("updating MR thread note %d: %w", noteID, withTokenHint(err))
	}
	return &note, nil
}

func (c *Client) ResolveMRDiscussion(ctx context.Context, project, mrIID, discussionID string, resolved bool) (*Discussion, error) {
	u := fmt.Sprintf("%s/%s", c.mrURL(project, mrIID, "discussions"), url.PathEscape(discussionID))
	var d Discussion
	_, err := c.do(ctx, http.MethodPut, u, map[string]bool{"resolved": resolved}, &d)
	if err != nil {
		return nil, fmt.Errorf("setting MR thread %s resolved=%t: %w", discussionID, resolved, withTokenHint(err))
	}
	return &d, nil
}

func withTokenHint(err error) error {
	var apiErr *APIError
	if errors.As(err, &apiErr) && (apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden) {
		apiErr.Hint = tokenHint
	}
	return err
}

func (c *Client) do(ctx context.Context, method, u string, body, out any) (http.Header, error) {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return nil, fmt.Errorf("encoding request: %w", err)
		}
	}
	for attempt := 1; ; attempt++ {
		hdr, data, err := c.roundTrip(ctx, method, u, payload)
		if err == nil {
			if out != nil {
				if err := json.Unmarshal(data, out); err != nil {
					return nil, fmt.Errorf("decoding response: %w", err)
				}
			}
			return hdr, nil
		}
		if attempt >= c.retry.attempts || !retryable(method, err) || ctx.Err() != nil {
			return nil, err
		}
		delay := c.retry.backoff(attempt, hdr)
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < delay {
			return nil, err
		}
		if err := c.retry.sleep(ctx, delay); err != nil {
			return nil, err
		}
	}
}

func (c *Client) roundTrip(ctx context.Context, method, u string, payload []byte) (http.Header, []byte, error) {
	var reqBody io.Reader
	if payload != nil {
		reqBody = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reqBody)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, &transportError{err: err}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return resp.Header, nil, &transportError{err: fmt.Errorf("reading response: %w", err)}
	}
	if len(data) > maxResponseBytes {
		return resp.Header, nil, fmt.Errorf("response from %s %s exceeds %d MiB", method, req.URL.Path, maxResponseBytes>>20)
	}
	if resp.StatusCode >= 400 {
		return resp.Header, nil, &APIError{StatusCode: resp.StatusCode, Message: apiMessage(data, resp.StatusCode)}
	}
	return resp.Header, data, nil
}

type transportError struct{ err error }

func (e *transportError) Error() string { return e.err.Error() }
func (e *transportError) Unwrap() error { return e.err }

func retryable(method string, err error) bool {
	idempotent := method == http.MethodGet || method == http.MethodPut
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusTooManyRequests:
			return true
		case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return idempotent
		}
		return false
	}
	var tErr *transportError
	if errors.As(err, &tErr) {
		return idempotent && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
	}
	return false
}

func (p retryPolicy) backoff(attempt int, hdr http.Header) time.Duration {
	if d, ok := parseRetryAfter(hdr.Get("Retry-After"), time.Now()); ok {
		return d
	}
	return min(p.base<<(attempt-1), p.max)
}

func parseRetryAfter(v string, now time.Time) (time.Duration, bool) {
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(t.Sub(now), 0), true
	}
	return 0, false
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
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
