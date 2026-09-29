package gitlabtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

const (
	BotUserID   int64 = 1
	HumanUserID int64 = 2
)

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

type Fault struct {
	Status     int
	RetryAfter string
}

type Fake struct {
	mu          sync.Mutex
	discussions []Discussion
	faults      []Fault
	nextID      int64

	PerPage      int
	RejectWrites bool
	RequireToken string
	Requests     atomic.Int64
}

func New() *Fake {
	return &Fake{PerPage: 20, nextID: 1}
}

func (f *Fake) Seed(bodies ...string) {
	for _, b := range bodies {
		f.add(false, Note{Body: b, Author: bot()})
	}
}

func (f *Fake) SeedForeign(body string) {
	f.add(false, Note{Body: body, Author: human()})
}

func (f *Fake) SeedSystem(body string) {
	f.add(false, Note{Body: body, System: true, Author: human()})
}

func (f *Fake) SeedThread(body string, resolved bool) string {
	return f.add(true, Note{Body: body, Author: bot(), Resolvable: true, Resolved: resolved})
}

func (f *Fake) Reply(discussionID, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.find(discussionID)
	if d == nil {
		return
	}
	d.Notes = append(d.Notes, Note{ID: f.nextID, Body: body, Author: human(), Resolvable: !d.IndividualNote, Resolved: d.Resolved()})
	f.nextID++
}

func (f *Fake) FailNext(faults ...Fault) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.faults = append(f.faults, faults...)
}

func (f *Fake) SetResolved(discussionID string, resolved bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if d := f.find(discussionID); d != nil {
		setResolved(d, resolved)
	}
}

func (f *Fake) Notes() []Note {
	f.mu.Lock()
	defer f.mu.Unlock()
	var notes []Note
	for _, d := range f.discussions {
		notes = append(notes, d.Notes...)
	}
	return notes
}

func (f *Fake) Discussions() []Discussion {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Discussion, len(f.discussions))
	for i, d := range f.discussions {
		out[i] = Discussion{ID: d.ID, IndividualNote: d.IndividualNote, Notes: append([]Note(nil), d.Notes...)}
	}
	return out
}

func (d Discussion) Resolved() bool {
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

func (f *Fake) add(thread bool, n Note) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	n.ID = f.nextID
	d := Discussion{ID: fmt.Sprintf("%040x", f.nextID), IndividualNote: !thread, Notes: []Note{n}}
	f.nextID++
	f.discussions = append(f.discussions, d)
	return d.ID
}

func (f *Fake) find(id string) *Discussion {
	for i := range f.discussions {
		if f.discussions[i].ID == id {
			return &f.discussions[i]
		}
	}
	return nil
}

func (f *Fake) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.Requests.Add(1)
		if f.injectFault(w) {
			return
		}
		if f.RequireToken != "" && r.Header.Get("PRIVATE-TOKEN") != f.RequireToken {
			writeError(w, http.StatusUnauthorized, "401 Unauthorized")
			return
		}
		segs := strings.Split(strings.TrimPrefix(r.URL.EscapedPath(), "/"), "/")
		if len(segs) == 1 && segs[0] == "user" && r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, bot())
			return
		}
		if len(segs) < 5 || segs[0] != "projects" || segs[2] != "merge_requests" {
			writeError(w, http.StatusNotFound, "404 Not Found")
			return
		}
		f.route(w, r, segs[4], segs[5:])
	})
}

func (f *Fake) route(w http.ResponseWriter, r *http.Request, collection string, rest []string) {
	switch {
	case collection == "notes" && r.Method == http.MethodPost && len(rest) == 0:
		f.createNote(w, r, false)
	case collection == "notes" && r.Method == http.MethodPut && len(rest) == 1:
		f.updateNote(w, r, "", rest[0])
	case collection == "discussions" && r.Method == http.MethodGet && len(rest) == 0:
		f.list(w, r)
	case collection == "discussions" && r.Method == http.MethodPost && len(rest) == 0:
		f.createNote(w, r, true)
	case collection == "discussions" && r.Method == http.MethodPut && len(rest) == 1:
		f.resolve(w, r, rest[0])
	case collection == "discussions" && r.Method == http.MethodPut && len(rest) == 3 && rest[1] == "notes":
		f.updateNote(w, r, rest[0], rest[2])
	default:
		writeError(w, http.StatusNotFound, "404 Not Found")
	}
}

func (f *Fake) injectFault(w http.ResponseWriter) bool {
	f.mu.Lock()
	if len(f.faults) == 0 {
		f.mu.Unlock()
		return false
	}
	fault := f.faults[0]
	f.faults = f.faults[1:]
	f.mu.Unlock()
	if fault.RetryAfter != "" {
		w.Header().Set("Retry-After", fault.RetryAfter)
	}
	writeError(w, fault.Status, http.StatusText(fault.Status))
	return true
}

func (f *Fake) list(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	all := f.Discussions()

	per := f.PerPage
	if per <= 0 {
		per = 20
	}
	totalPages := (len(all) + per - 1) / per
	start := min((page-1)*per, len(all))
	end := min(start+per, len(all))

	w.Header().Set("x-total-pages", strconv.Itoa(totalPages))
	if page < totalPages {
		w.Header().Set("x-next-page", strconv.Itoa(page+1))
	} else {
		w.Header().Set("x-next-page", "")
	}
	writeJSON(w, http.StatusOK, all[start:end])
}

func (f *Fake) createNote(w http.ResponseWriter, r *http.Request, thread bool) {
	if f.RejectWrites {
		writeError(w, http.StatusForbidden, "403 Forbidden")
		return
	}
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}
	id := f.add(thread, Note{Body: body, Author: bot(), Resolvable: thread})
	f.mu.Lock()
	d := *f.find(id)
	f.mu.Unlock()
	if thread {
		writeJSON(w, http.StatusCreated, d)
		return
	}
	writeJSON(w, http.StatusCreated, d.Notes[0])
}

func (f *Fake) updateNote(w http.ResponseWriter, r *http.Request, discussionID, idSeg string) {
	if f.RejectWrites {
		writeError(w, http.StatusForbidden, "403 Forbidden")
		return
	}
	id, err := strconv.ParseInt(idSeg, 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "404 Not Found")
		return
	}
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.discussions {
		d := &f.discussions[i]
		if discussionID != "" && d.ID != discussionID {
			continue
		}
		for j := range d.Notes {
			if d.Notes[j].ID != id {
				continue
			}
			if d.Notes[j].Author.ID != BotUserID {
				writeError(w, http.StatusForbidden, "403 Forbidden")
				return
			}
			d.Notes[j].Body = body
			writeJSON(w, http.StatusOK, d.Notes[j])
			return
		}
	}
	writeError(w, http.StatusNotFound, "404 Note Not Found")
}

func (f *Fake) resolve(w http.ResponseWriter, r *http.Request, discussionID string) {
	if f.RejectWrites {
		writeError(w, http.StatusForbidden, "403 Forbidden")
		return
	}
	var req struct {
		Resolved *bool `json:"resolved"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Resolved == nil {
		writeError(w, http.StatusBadRequest, `400 (Bad request) "resolved" not given`)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.find(discussionID)
	if d == nil {
		writeError(w, http.StatusNotFound, "404 Discussion Not Found")
		return
	}
	if d.IndividualNote {
		writeError(w, http.StatusBadRequest, "400 Bad request - discussion is not resolvable")
		return
	}
	setResolved(d, *req.Resolved)
	writeJSON(w, http.StatusOK, *d)
}

func setResolved(d *Discussion, resolved bool) {
	for i := range d.Notes {
		if d.Notes[i].Resolvable {
			d.Notes[i].Resolved = resolved
		}
	}
}

func bot() User   { return User{ID: BotUserID, Username: "project_42_bot"} }
func human() User { return User{ID: HumanUserID, Username: "reviewer"} }

func decodeBody(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Body == "" {
		writeError(w, http.StatusBadRequest, `400 (Bad request) "body" not given`)
		return "", false
	}
	return req.Body, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		panic(fmt.Sprintf("gitlabtest: encode response: %v", err))
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"message": message})
}
