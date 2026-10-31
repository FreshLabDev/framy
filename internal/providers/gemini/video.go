// SPDX-License-Identifier: Apache-2.0
package gemini

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/jobs"
)

// A video is Gemini Omni's, a job at Google (docs/design.md, Jobs): POST
// /interactions with background true answers at once with the interaction's
// id, GET /interactions/{id} says where it is, and a finished one holds the
// video inline in base64, however it was asked for. The queue saves the id
// the moment Submit returns it and asks Poll every 30 s.
//
// Google keeps a background interaction (store true), for 55 days on a paid
// project or what the project sets in AI Studio, and framy leaves it there:
// it is what previous_interaction_id continues.

// The statuses of an interaction.
const (
	statusInProgress           = "in_progress"
	statusCompleted            = "completed"
	statusFailed               = "failed"
	statusCancelled            = "cancelled"
	statusIncomplete           = "incomplete"
	statusRequiresAction       = "requires_action"
	statusRequiresContinuation = "requires_continuation"
)

// startVideo sends the paid request. The answer is the interaction's id,
// which the queue writes down before anything else happens.
func (p *Provider) startVideo(ctx context.Context, key string, j jobs.Job, body []byte) (jobs.Submission, error) {
	raw, err := p.do(ctx, key, http.MethodPost, "/interactions", body, p.cfg.MaxVideoAnswer)
	if err != nil {
		return jobs.Submission{}, err
	}
	var r interaction
	if err := json.Unmarshal(raw, &r); err != nil || !interactionID.MatchString(r.ID) {
		// Google probably took the request: do not send it again.
		return jobs.Submission{}, &jobs.Error{Class: jobs.ProviderFailed, Err: errors.New("the answer to the video request has no interaction id; the run may have been paid")}
	}
	sub := jobs.Submission{ProviderJobID: r.ID}
	if r.Status == statusInProgress || r.Status == "" {
		return sub, nil
	}
	// Already over: refused at once, or done before the first poll.
	out, err := p.videoOutcome(ctx, key, j.Offer, r, raw)
	if err != nil || out.Running {
		// The id is saved by the queue; the poll asks again.
		return sub, nil
	}
	sub.Outcome = &out
	return sub, nil
}

// pollVideo asks where the video is. It costs nothing.
func (p *Provider) pollVideo(ctx context.Context, j jobs.Job) (jobs.Outcome, error) {
	if !interactionID.MatchString(j.ProviderJobID) {
		return jobs.Outcome{}, &jobs.Error{Class: jobs.Internal, Err: errors.New("the job has no usable Gemini interaction id")}
	}
	key, err := p.key(ctx, j)
	if err != nil {
		return jobs.Outcome{}, err
	}
	raw, err := p.do(ctx, key, http.MethodGet, "/interactions/"+url.PathEscape(j.ProviderJobID), nil, p.cfg.MaxVideoAnswer)
	var pe *jobs.Error
	var big *tooLargeError
	switch {
	case errors.As(err, &big):
		// Paid and finished, and too large to keep: nothing to ask again.
		return jobs.Outcome{Err: &jobs.Error{Class: jobs.Storage, Err: big}}, nil
	case errors.As(err, &pe) && pe.Class == jobs.Refused:
		// A 404 or a 400 on a GET is no verdict on a run that may be paid
		// and finishing: ask again, and let the hour's deadline end it.
		return jobs.Outcome{}, fmt.Errorf("poll the interaction: %w", pe.Err)
	case err != nil:
		return jobs.Outcome{}, err
	}
	var r interaction
	if err := json.Unmarshal(raw, &r); err != nil {
		return jobs.Outcome{}, &jobs.Error{Class: jobs.ProviderFailed, Err: errors.New("the interaction's status cannot be read")}
	}
	return p.videoOutcome(ctx, key, j.Offer, r, raw)
}

// videoOutcome reads an interaction. A status it does not know is taken for
// a run still going: the queue gives up on it after an hour. An error means
// the result could not be fetched yet, and the queue asks again.
func (p *Provider) videoOutcome(ctx context.Context, key string, of catalog.Offer, r interaction, raw []byte) (jobs.Outcome, error) {
	var out jobs.Outcome
	switch r.Status {
	case statusCompleted:
	case statusFailed, statusCancelled, statusIncomplete:
		// Google bills what a run used even when it ends without a video.
		out.CostUSD = cost(pricesOf(of), r.Usage)
		out.Err = noResult(r, raw, collect(r, "video").text, key, catalog.Video)
		return out, nil
	case statusRequiresAction, statusRequiresContinuation:
		out.Err = &jobs.Error{Class: jobs.ProviderFailed, Err: fmt.Errorf("gemini stopped the video as %s, which framy cannot answer", r.Status)}
		return out, nil
	default:
		out.Running = true
		return out, nil
	}
	m := collect(r, "video")
	out.CostUSD = cost(pricesOf(of), r.Usage)
	if !trustworthy(r.Usage) {
		out.CostUSD = nil
	}
	if len(m.media) == 0 {
		out.Err = noResult(r, raw, m.text, key, catalog.Video)
		return out, nil
	}
	for i, b := range m.media {
		res, err := p.video(ctx, key, b)
		var big *tooLargeError
		var pe *jobs.Error
		switch {
		case errors.As(err, &big):
			out.Results = nil
			out.Err = &jobs.Error{Class: jobs.Storage, Err: fmt.Errorf("video %d of the answer: %w", i+1, err)}
			return out, nil
		case errors.As(err, &pe):
			out.Results = nil
			out.Err = pe
			return out, nil
		case err != nil:
			return jobs.Outcome{}, err
		}
		out.Results = append(out.Results, res)
	}
	out.Grounding = m.grounding
	return out, nil
}

// video decodes one video of the answer, or downloads it when Google gave a
// link instead.
func (p *Provider) video(ctx context.Context, key string, b block) (jobs.Result, error) {
	if b.Data == "" {
		if b.URI == "" {
			return jobs.Result{}, &jobs.Error{Class: jobs.ProviderFailed, Err: errors.New("a video with no data")}
		}
		return p.download(ctx, key, b.URI)
	}
	if int64(base64.StdEncoding.DecodedLen(len(b.Data))) > p.cfg.MaxVideo+2 {
		return jobs.Result{}, &tooLargeError{what: "the video", limit: p.cfg.MaxVideo}
	}
	data, err := base64.StdEncoding.DecodeString(b.Data)
	switch {
	case err != nil:
		return jobs.Result{}, &jobs.Error{Class: jobs.ProviderFailed, Err: fmt.Errorf("decode base64: %w", err)}
	case len(data) == 0:
		return jobs.Result{}, &jobs.Error{Class: jobs.ProviderFailed, Err: errors.New("an empty video")}
	case int64(len(data)) > p.cfg.MaxVideo:
		return jobs.Result{}, &tooLargeError{what: "the video", limit: p.cfg.MaxVideo}
	}
	return jobs.Result{ContentType: videoType(b.MimeType, data), Data: data}, nil
}

// videoType is a result's type: Google's when it names a video, else what
// the bytes look like, else MP4, which is all Omni makes.
func videoType(given string, data []byte) string {
	if t, _, err := mime.ParseMediaType(given); err == nil && strings.HasPrefix(t, "video/") {
		return t
	}
	if t := http.DetectContentType(data); strings.HasPrefix(t, "video/") {
		return t
	}
	return "video/mp4"
}

// fileLink finds the name of a Files API file in a link Gemini gave, as
// https://generativelanguage.googleapis.com/v1beta/files/abc-123:download?alt=media.
var fileLink = regexp.MustCompile(`/files/([a-z0-9][a-z0-9-]{0,62})(?::download)?(?:[?#]|$)`)

// download fetches a video Google left as a Files API file, with the key. The
// link in the answer is not followed: only the file's name is taken from it,
// and the key goes to the base this adapter was told to use, never to a host
// an answer names. A redirect away from that host loses the key.
func (p *Provider) download(ctx context.Context, key, link string) (jobs.Result, error) {
	m := fileLink.FindStringSubmatch(link)
	if m == nil {
		return jobs.Result{}, &jobs.Error{Class: jobs.ProviderFailed, Err: errors.New("the video came as a link framy cannot read")}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.cfg.Base+"/files/"+m[1]+":download?alt=media", nil)
	if err != nil {
		return jobs.Result{}, &jobs.Error{Class: jobs.Internal, Err: err}
	}
	p.authorize(req, key)
	req.Header.Set("Accept", "video/*, */*;q=0.5")
	client := *p.cfg.Fetch
	check := client.CheckRedirect
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if next.URL.Host != via[0].URL.Host {
			next.Header.Del("x-goog-api-key")
		}
		if check != nil {
			return check(next, via)
		}
		if len(via) >= 5 {
			return errors.New("more than five redirects")
		}
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		// A redirect may have led to a signed link, and the error would name it.
		return jobs.Result{}, transportError(withoutURL(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		pe := statusError(resp.StatusCode, resp.Header, data, key)
		if pe.Class == jobs.Refused || pe.Class == jobs.ProviderFailed {
			// The file of a finished run exists; a 404 now is more likely one
			// still being written than one that is gone.
			return jobs.Result{}, fmt.Errorf("download the video: %w", pe.Err)
		}
		return jobs.Result{}, pe
	}
	limit := p.cfg.MaxVideo
	if resp.ContentLength > limit {
		return jobs.Result{}, &tooLargeError{what: "the video", limit: limit}
	}
	var buf bytes.Buffer
	if resp.ContentLength > 0 {
		buf.Grow(int(resp.ContentLength))
	}
	if _, err := buf.ReadFrom(io.LimitReader(resp.Body, limit+1)); err != nil {
		return jobs.Result{}, fmt.Errorf("download the video: %w", withoutURL(err))
	}
	switch {
	case int64(buf.Len()) > limit:
		return jobs.Result{}, &tooLargeError{what: "the video", limit: limit}
	case buf.Len() == 0:
		return jobs.Result{}, errors.New("download the video: an empty file")
	}
	contentType := resp.Header.Get("Content-Type")
	if t, _, _ := mime.ParseMediaType(contentType); strings.HasPrefix(t, "text/") || t == "application/json" {
		return jobs.Result{}, fmt.Errorf("download the video: Google sent %s, not a video", t)
	}
	return jobs.Result{ContentType: videoType(contentType, buf.Bytes()), Data: buf.Bytes()}, nil
}
