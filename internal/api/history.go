// SPDX-License-Identifier: Apache-2.0
package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/jobs"
)

// The history's pages.
const (
	defaultPage = 30
	maxPage     = 100
	// maxQuery is the longest search, in characters.
	maxQuery = 200
)

// historyJSON is a page of GET /api/jobs. Next is the before of the next
// page, absent on the last one.
type historyJSON struct {
	Jobs []jobJSON `json:"jobs"`
	Next int64     `json:"next,omitempty"`
}

// history answers GET /api/jobs: the person's jobs that were queued, newest
// first, each as GET /api/jobs/{id} gives it. kind is image or video, q a
// part of the prompt in any case, before the next of the previous page and
// limit up to 100 (30 by default). A draft that was never generated and a
// deleted job are not in it.
func (a *API) history(w http.ResponseWriter, r *http.Request) {
	user, ok := a.person(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := Filter{Limit: defaultPage}
	switch k := q.Get("kind"); k {
	case "":
	case string(catalog.Image), string(catalog.Video):
		f.Kind = catalog.Kind(k)
	default:
		badRequest("kind", `kind is "image" or "video"`).write(w)
		return
	}
	f.Query = strings.TrimSpace(q.Get("q"))
	if utf8.RuneCountInString(f.Query) > maxQuery {
		badRequest("q", "the search is at most 200 characters").write(w)
		return
	}
	if v := q.Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			badRequest("before", "before is the next of the previous page").write(w)
			return
		}
		f.Before = n
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxPage {
			badRequest("limit", "limit is 1 to 100").write(w)
			return
		}
		f.Limit = n
	}
	page := f.Limit
	f.Limit++ // one more says whether there is a next page
	states, err := a.data.List(r.Context(), user, f)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	// A page ends where a batch does, so the library shows one press as one
	// run: it takes the rest of the batch it would cut.
	cut := min(page, len(states))
	for cut < len(states) && sameBatch(states[cut-1], states[cut]) {
		cut++
	}
	if cut == len(states) && len(states) > page {
		g := f
		g.Before, g.Limit = states[len(states)-1].ID, maxImageFrames+1
		more, err := a.data.List(r.Context(), user, g)
		if err != nil {
			a.internal(w, r, err)
			return
		}
		for len(more) > 0 && sameBatch(states[len(states)-1], more[0]) {
			states, more = append(states, more[0]), more[1:]
		}
		cut = len(states)
		states = append(states, more...)
	}
	out := historyJSON{}
	if len(states) > cut {
		states = states[:cut]
		out.Next = states[cut-1].ID
	}
	out.Jobs = a.renderAll(r.Context(), user, states, a.language(r.Context(), r, user))
	writeJSON(w, http.StatusOK, out)
}

// remove answers DELETE /api/jobs/{id}: the job's results go, with the
// uploads no other job takes, and the job leaves the history. A job that has
// not ended is refused with 409 job_active: it is stopped first, or ends on
// its own. What the free allowance counts stays (data.Remove).
func (a *API) remove(w http.ResponseWriter, r *http.Request) {
	user, ok := a.person(w, r)
	if !ok {
		return
	}
	id, ok := pathID(r)
	if !ok {
		notFound(w)
		return
	}
	ctx := r.Context()
	st, err := a.data.State(ctx, user, id)
	switch {
	case errors.Is(err, ErrNotFound):
		notFound(w)
		return
	case err != nil:
		a.internal(w, r, err)
		return
	case !st.Ended():
		newError(http.StatusConflict, codeJobActive, "the job has not ended; stop it or wait for it first").write(w)
		return
	}
	if a.cfg.Files != nil {
		if _, err := a.cfg.Files.DeleteJobFiles(ctx, user, id); err != nil {
			a.log.Error("delete the files of a job", "job", id, "error", err)
			newError(http.StatusServiceUnavailable, codeStorageDown, "the files could not be deleted just now; try again").write(w)
			return
		}
	} else if len(st.Inputs) > 0 || len(st.Results) > 0 {
		newError(http.StatusServiceUnavailable, codeStorageDown, "framy keeps no files on this server, so they cannot be deleted").write(w)
		return
	}
	switch err := a.data.Remove(ctx, user, id); {
	case errors.Is(err, ErrNotFound):
		notFound(w)
	case errors.Is(err, ErrActive):
		newError(http.StatusConflict, codeJobActive, "the job has not ended; stop it or wait for it first").write(w)
	case err != nil:
		a.internal(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// cancel answers POST /api/jobs/{id}/cancel: a queued job is cancelled and
// comes back as GET gives it. A running one cannot be stopped at the provider
// and is refused with 409 job_running: it finishes and goes to the history.
// A draft belongs to its preview in the chat, which answers it, so it is
// refused with 409 job_draft. A job that ended comes back as it is.
func (a *API) cancel(w http.ResponseWriter, r *http.Request) {
	user, ok := a.person(w, r)
	if !ok {
		return
	}
	id, ok := pathID(r)
	if !ok {
		notFound(w)
		return
	}
	ctx := r.Context()
	st, err := a.data.State(ctx, user, id)
	switch {
	case errors.Is(err, ErrNotFound):
		notFound(w)
		return
	case err != nil:
		a.internal(w, r, err)
		return
	case st.Status == jobs.Draft:
		newError(http.StatusConflict, codeJobDraft, "the job waits for its preview in the chat; answer it there").write(w)
		return
	case st.Status == jobs.Queued:
		switch err := a.cfg.Queue.Cancel(ctx, id); {
		case errors.Is(err, jobs.ErrRunning):
			newError(http.StatusConflict, codeJobRunning, "the job is running and cannot be stopped; it ends on its own").write(w)
			return
		case err != nil:
			a.internal(w, r, err)
			return
		}
		if st, err = a.data.State(ctx, user, id); err != nil {
			a.internal(w, r, err)
			return
		}
	case st.Status == jobs.Running:
		newError(http.StatusConflict, codeJobRunning, "the job is running and cannot be stopped; it ends on its own").write(w)
		return
	}
	writeJSON(w, http.StatusOK, a.render(ctx, user, st, a.language(ctx, r, user)))
}

// sameBatch reports whether two jobs are of the same press of Create.
func sameBatch(a, b State) bool { return a.Batch != "" && a.Batch == b.Batch }
