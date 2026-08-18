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

type Note struct {
	ID     int64  `json:"id"`
	Body   string `json:"body"`
	System bool   `json:"system"`
}

type Fake struct {
	mu     sync.Mutex
	notes  []Note
	nextID int64

	PerPage      int
	RejectWrites bool
	RequireToken string
	Requests     atomic.Int64
}

func New() *Fake {
	return &Fake{PerPage: 20, nextID: 1}
}

func (f *Fake) Seed(bodies ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, b := range bodies {
		f.notes = append(f.notes, Note{ID: f.nextID, Body: b})
		f.nextID++
	}
}

func (f *Fake) SeedSystem(body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notes = append(f.notes, Note{ID: f.nextID, Body: body, System: true})
	f.nextID++
}

func (f *Fake) Notes() []Note {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Note(nil), f.notes...)
}

func (f *Fake) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.Requests.Add(1)
		if f.RequireToken != "" && r.Header.Get("PRIVATE-TOKEN") != f.RequireToken {
			writeError(w, http.StatusUnauthorized, "401 Unauthorized")
			return
		}
		segs := strings.Split(strings.TrimPrefix(r.URL.EscapedPath(), "/"), "/")
		if len(segs) < 5 || segs[0] != "projects" || segs[2] != "merge_requests" || segs[4] != "notes" {
			writeError(w, http.StatusNotFound, "404 Not Found")
			return
		}
		switch {
		case r.Method == http.MethodGet && len(segs) == 5:
			f.list(w, r)
		case r.Method == http.MethodPost && len(segs) == 5:
			f.create(w, r)
		case r.Method == http.MethodPut && len(segs) == 6:
			f.update(w, r, segs[5])
		default:
			writeError(w, http.StatusMethodNotAllowed, "405 Method Not Allowed")
		}
	})
}

func (f *Fake) list(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	f.mu.Lock()
	notes := append([]Note(nil), f.notes...)
	f.mu.Unlock()

	per := f.PerPage
	if per <= 0 {
		per = 20
	}
	totalPages := (len(notes) + per - 1) / per
	start := min((page-1)*per, len(notes))
	end := min(start+per, len(notes))

	w.Header().Set("x-total-pages", strconv.Itoa(totalPages))
	if page < totalPages {
		w.Header().Set("x-next-page", strconv.Itoa(page+1))
	} else {
		w.Header().Set("x-next-page", "")
	}
	writeJSON(w, http.StatusOK, notes[start:end])
}

func (f *Fake) create(w http.ResponseWriter, r *http.Request) {
	if f.RejectWrites {
		writeError(w, http.StatusForbidden, "403 Forbidden")
		return
	}
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}
	f.mu.Lock()
	note := Note{ID: f.nextID, Body: body}
	f.nextID++
	f.notes = append(f.notes, note)
	f.mu.Unlock()
	writeJSON(w, http.StatusCreated, note)
}

func (f *Fake) update(w http.ResponseWriter, r *http.Request, idSeg string) {
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
	for i := range f.notes {
		if f.notes[i].ID == id {
			f.notes[i].Body = body
			writeJSON(w, http.StatusOK, f.notes[i])
			return
		}
	}
	writeError(w, http.StatusNotFound, "404 Note Not Found")
}

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
