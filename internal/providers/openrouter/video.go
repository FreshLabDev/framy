// SPDX-License-Identifier: Apache-2.0
package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/jobs"
)

// A video is a job at OpenRouter (docs/design.md, Jobs): POST /videos answers
// 202 with the job's id, GET /videos/{id} says where it is, and a finished
// one is downloaded from /videos/{id}/content with the key. The queue saves
// the id the moment Submit returns it and asks Poll every 30 s; a webhook to
// the callback_url only brings a poll forward.

// The statuses of a video job.
const (
	videoPending    = "pending"
	videoInProgress = "in_progress"
	videoCompleted  = "completed"
	videoFailed     = "failed"
	videoCancelled  = "cancelled"
	videoExpired    = "expired"
)

// videoReserved are the fields framy fills itself; no setting may set them.
// There is no "user": the Videos API documents no such field, and a field it
// does not know may turn every request away.
var videoReserved = map[string]bool{
	"model": true, "prompt": true, "provider": true, "input_references": true,
	"callback_url": true, "user": true, "stream": true, "session_id": true, "trace": true,
}

// A setting that is not a catalog parameter.
const (
	// framesSetting names which of the job's first inputs are frames: a list
	// of the model's frame types, such as ["first_frame", "last_frame"], one
	// per input in order. Every input after them is a reference, by its type.
	// The API takes frames or references, not both (it keeps the frames and
	// drops the rest), so a run that has both is refused.
	framesSetting = "frame_images"
	// previousSetting continues or edits a video OpenRouter made, on the same
	// model.
	previousSetting = "previous_job_id"
)

// videoRequest builds the body of POST /videos. What framy can tell is wrong
// comes back as refused, so nothing is paid for it. A pass-through setting
// (a negative prompt, say) is refused too: it goes under
// provider.options.<slug>, and the video catalog names no slug.
func videoRequest(j jobs.Job, callbackURL string) (map[string]any, error) {
	of := j.Offer
	if of.ProviderModelID == "" {
		return nil, refused("the offer %s has no model id", j.OfferID)
	}
	if strings.TrimSpace(j.Prompt) == "" && len(j.Inputs) == 0 {
		return nil, refused("the prompt is empty and there is nothing to work from")
	}
	body := map[string]any{"model": of.ProviderModelID}
	if strings.TrimSpace(j.Prompt) != "" {
		body["prompt"] = j.Prompt
	}

	var frames []string
	for _, name := range slices.Sorted(maps.Keys(j.Settings)) {
		v := j.Settings[name]
		switch {
		case v == nil:
		case name == framesSetting:
			var err error
			if frames, err = frameTypes(v); err != nil {
				return nil, err
			}
		case name == previousSetting:
			id, ok := v.(string)
			if !ok || !jobID.MatchString(id) {
				return nil, refused("%s is not the id of a video job", name)
			}
			body[name] = id
		case videoReserved[name]:
			return nil, refused("%s is not a setting", name)
		default:
			p, ok := of.Params[name]
			switch {
			case !ok:
				return nil, refused("%s is not a setting of %s", name, of.ID)
			case p.Passthrough:
				return nil, refused("%s is a setting of the model's own provider, which framy cannot address yet", name)
			case !accepts(name, p, v):
				return nil, refused("%s=%v is not allowed for %s", name, v, of.ID)
			}
			body[name] = normalize(name, v)
		}
	}
	if _, bySize := body["size"]; bySize {
		for _, other := range []string{"resolution", "aspect_ratio"} {
			if _, both := body[other]; both {
				return nil, refused("size replaces %s; set one or the other", other)
			}
		}
	}

	frameImages, references, err := videoInputs(of.ID, of.Params[framesSetting], frames, j.Inputs)
	if err != nil {
		return nil, err
	}
	if len(frameImages) > 0 {
		body[framesSetting] = frameImages
	}
	if len(references) > 0 {
		body["input_references"] = references
	}
	if callbackURL != "" {
		body["callback_url"] = callbackURL
	}
	return body, nil
}

// frameTypes reads the frame_images setting.
func frameTypes(v any) ([]string, error) {
	switch list := v.(type) {
	case []string:
		return list, nil
	case []any:
		out := make([]string, len(list))
		for i, e := range list {
			s, ok := e.(string)
			if !ok {
				return nil, refused("%s must list frame types such as first_frame", framesSetting)
			}
			out[i] = s
		}
		return out, nil
	}
	return nil, refused("%s must list frame types such as first_frame", framesSetting)
}

// videoInputs turns a job's files into the API's frames and references.
func videoInputs(offer string, takes catalog.Param, frames []string, inputs []jobs.Input) (frameImages, references []map[string]any, err error) {
	if len(frames) > len(inputs) {
		return nil, nil, refused("%s names %d frames, and the run has %d files", framesSetting, len(frames), len(inputs))
	}
	seen := map[string]bool{}
	for _, f := range frames {
		switch {
		case takes.Type == "":
			return nil, nil, refused("%s takes no frame images", offer)
		case !accepts(framesSetting, takes, f):
			return nil, nil, refused("%s does not take a frame of type %q", offer, f)
		case seen[f]:
			return nil, nil, refused("two frames of type %q", f)
		}
		seen[f] = true
	}
	for i, in := range inputs {
		kind, ok := inputKind(in.ContentType)
		switch {
		case !ok:
			return nil, nil, refused("input %d is %q, which OpenRouter's Videos API does not take", i+1, in.ContentType)
		case in.URL == "":
			return nil, nil, &jobs.Error{Class: jobs.Storage, Err: fmt.Errorf("input %d has no link", i+1)}
		case i < len(frames) && kind != "image":
			return nil, nil, refused("input %d is a %s, and a frame is an image", i+1, kind)
		}
		link := map[string]any{"url": in.URL}
		if i < len(frames) {
			frameImages = append(frameImages, map[string]any{"type": "image_url", "image_url": link, "frame_type": frames[i]})
			continue
		}
		references = append(references, map[string]any{"type": kind + "_url", kind + "_url": link})
	}
	if len(frameImages) > 0 && len(references) > 0 {
		return nil, nil, refused("OpenRouter drops the references when frames come along, so a run with both is not sent")
	}
	return frameImages, references, nil
}

// inputKind is what OpenRouter calls a file by its type: image, video or
// audio.
func inputKind(contentType string) (string, bool) {
	kind, _, _ := strings.Cut(contentType, "/")
	return kind, kind == "image" || kind == "video" || kind == "audio"
}

// jobID is what OpenRouter's video job ids look like (gen-vid-<unix>-<random>),
// loosely: the id goes into a path, so only a plain run of characters is
// taken.
var jobID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,199}$`)

// videoJob is OpenRouter's video job, the answer of both POST /videos and
// GET /videos/{id}.
type videoJob struct {
	ID     string          `json:"id"`
	Status string          `json:"status"`
	Error  json.RawMessage `json:"error"`
	// The results: unsigned_urls need the key; urls are signed. Only their
	// number is used, since the video is downloaded from /content.
	UnsignedURLs []string `json:"unsigned_urls"`
	URLs         []string `json:"urls"`
	Usage        *struct {
		Cost *float64 `json:"cost"`
	} `json:"usage"`
}

// submitVideo sends the paid request. The answer is the job's id, which the
// queue writes down before anything else happens.
func (p *Provider) submitVideo(ctx context.Context, j jobs.Job) (jobs.Submission, error) {
	var callback string
	if p.cfg.Callbacks.Enabled() {
		callback = p.cfg.Callbacks.URL(j.ID)
	}
	body, err := videoRequest(j, callback)
	if err != nil {
		return jobs.Submission{}, err
	}
	key, err := p.key(ctx, j)
	if err != nil {
		return jobs.Submission{}, err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return jobs.Submission{}, &jobs.Error{Class: jobs.Internal, Err: err}
	}
	req, err := p.newRequest(ctx, key, http.MethodPost, p.cfg.Base+"/videos", bytes.NewReader(raw))
	if err != nil {
		return jobs.Submission{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.cfg.HTTP.Do(req)
	if err != nil {
		return jobs.Submission{}, transportError(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return jobs.Submission{}, readError(ctx, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return jobs.Submission{}, statusError(resp.StatusCode, resp.Header, data)
	}
	var r videoJob
	if err := json.Unmarshal(data, &r); err != nil || !jobID.MatchString(r.ID) {
		// OpenRouter probably took the request: do not send it again.
		return jobs.Submission{}, &jobs.Error{Class: jobs.ProviderFailed, Err: errors.New("the answer to the video request has no job id; the run may have been paid")}
	}
	sub := jobs.Submission{ProviderJobID: r.ID}
	if r.Status == videoPending || r.Status == videoInProgress || r.Status == "" {
		return sub, nil
	}
	// Already over: failed at once, or done before the first poll.
	out, err := p.outcome(ctx, key, r)
	if err != nil {
		// The id is saved by the queue; the poll tries again.
		return sub, nil
	}
	sub.Outcome = &out
	return sub, nil
}

// pollVideo asks where the job is. It costs nothing.
func (p *Provider) pollVideo(ctx context.Context, j jobs.Job) (jobs.Outcome, error) {
	if !jobID.MatchString(j.ProviderJobID) {
		return jobs.Outcome{}, &jobs.Error{Class: jobs.Internal, Err: errors.New("the job has no usable OpenRouter job id")}
	}
	key, err := p.key(ctx, j)
	if err != nil {
		return jobs.Outcome{}, err
	}
	req, err := p.newRequest(ctx, key, http.MethodGet, p.cfg.Base+"/videos/"+url.PathEscape(j.ProviderJobID), nil)
	if err != nil {
		return jobs.Outcome{}, err
	}
	resp, err := p.cfg.HTTP.Do(req)
	if err != nil {
		return jobs.Outcome{}, transportError(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return jobs.Outcome{}, readError(ctx, err)
	}
	if resp.StatusCode != http.StatusOK {
		pe := statusError(resp.StatusCode, resp.Header, data)
		if pe.Class == jobs.Refused {
			// A 404 or a 400 on a GET is no verdict on a run that may be paid
			// and finishing: ask again, and let the hour's deadline end it.
			return jobs.Outcome{}, fmt.Errorf("poll the video job: %w", pe.Err)
		}
		return jobs.Outcome{}, pe
	}
	var r videoJob
	if err := json.Unmarshal(data, &r); err != nil {
		return jobs.Outcome{}, &jobs.Error{Class: jobs.ProviderFailed, Err: errors.New("the video job's status cannot be read")}
	}
	return p.outcome(ctx, key, r)
}

// outcome reads a job object. A status it does not know is taken for a run
// still going: the queue gives up on it after an hour.
func (p *Provider) outcome(ctx context.Context, key string, r videoJob) (jobs.Outcome, error) {
	var out jobs.Outcome
	if r.Usage != nil {
		out.CostUSD = r.Usage.Cost
	}
	switch r.Status {
	case videoCompleted:
		n := max(len(r.UnsignedURLs), len(r.URLs))
		if n == 0 {
			out.Err = &jobs.Error{Class: jobs.ProviderFailed, Err: errors.New("OpenRouter finished the video job with no video")}
			return out, nil
		}
		for i := range n {
			res, err := p.download(ctx, key, r.ID, i)
			var big *tooLargeError
			switch {
			case errors.As(err, &big):
				// Paid and finished, and too large to keep: nothing to try again.
				out.Results = nil
				out.Err = &jobs.Error{Class: jobs.Storage, Err: err}
				return out, nil
			case err != nil:
				return jobs.Outcome{}, err
			}
			out.Results = append(out.Results, res)
		}
	case videoFailed, videoCancelled, videoExpired:
		out.Err = videoFailure(r)
	default:
		out.Running = true
	}
	return out, nil
}

// videoFailure says why a job ended without a video.
func videoFailure(r videoJob) *jobs.Error {
	switch r.Status {
	case videoCancelled:
		return &jobs.Error{Class: jobs.ProviderFailed, Err: errors.New("OpenRouter cancelled the video job")}
	case videoExpired:
		return &jobs.Error{Class: jobs.Timeout, Err: errors.New("OpenRouter's video job expired before it finished")}
	}
	var text string
	if err := json.Unmarshal(r.Error, &text); err != nil {
		// Not a string: an object like the one of every other error.
		var b errorBody
		if json.Unmarshal(r.Error, &b) == nil && (b.Message != "" || b.Code != nil || len(b.Metadata) > 0) {
			return bodyError(&b)
		}
	}
	text = strings.TrimSpace(text)
	if text == "" {
		text = "no reason given"
	}
	if len(text) > 300 {
		text = text[:300]
	}
	e := &APIError{InBody: true, Message: text}
	class := jobs.ProviderFailed
	if saysModeration(text) {
		e.Type, class = "content_policy_violation", jobs.Moderation
	}
	return &jobs.Error{Class: class, Err: e}
}

// moderationPhrases are what a plain-text failure says when a content filter
// stopped the video. They are narrow on purpose: calling a provider failure a
// moderation costs a person their free video, and the other mistake costs
// nothing, since a failed run is not billed.
var moderationPhrases = []string{"content policy", "content_policy", "moderation", "flagged", "safety filter", "safety system", "responsible ai"}

func saysModeration(text string) bool {
	text = strings.ToLower(text)
	return slices.ContainsFunc(moderationPhrases, func(p string) bool { return strings.Contains(text, p) })
}

// tooLargeError: a finished video above the limit.
type tooLargeError struct{ limit int64 }

func (e *tooLargeError) Error() string {
	return fmt.Sprintf("the video is larger than %d MiB, which framy keeps in memory while it copies it", e.limit>>20)
}

// download fetches result i of a finished job with the key. The URLs in the
// job object are not used: unsigned ones need the key, which must go only to
// OpenRouter, and signed ones are secrets. /content serves the same file from
// the base this adapter was told to use.
func (p *Provider) download(ctx context.Context, key, id string, index int) (jobs.Result, error) {
	link := fmt.Sprintf("%s/videos/%s/content?index=%d", p.cfg.Base, url.PathEscape(id), index)
	req, err := p.newRequest(ctx, key, http.MethodGet, link, nil)
	if err != nil {
		return jobs.Result{}, err
	}
	req.Header.Set("Accept", "video/*, */*;q=0.5")
	client := p.cfg.Fetch
	if client == nil {
		client = p.cfg.HTTP
	}
	resp, err := client.Do(req)
	if err != nil {
		// A redirect may have led to a signed link, and the error would name it.
		return jobs.Result{}, transportError(withoutURL(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		pe := statusError(resp.StatusCode, resp.Header, data)
		if pe.Class == jobs.Refused || pe.Class == jobs.ProviderFailed {
			// A job that is finished has a video; a 404 now is more likely a
			// copy still on its way than a video that is gone.
			return jobs.Result{}, fmt.Errorf("download the video: %w", pe.Err)
		}
		return jobs.Result{}, pe
	}
	contentType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if strings.HasPrefix(contentType, "text/") || contentType == "application/json" {
		return jobs.Result{}, fmt.Errorf("download the video: OpenRouter sent %s, not a video", contentType)
	}
	limit := p.cfg.MaxVideo
	if resp.ContentLength > limit {
		return jobs.Result{}, &tooLargeError{limit}
	}
	var buf bytes.Buffer
	if resp.ContentLength > 0 {
		buf.Grow(int(resp.ContentLength))
	}
	if _, err := buf.ReadFrom(io.LimitReader(resp.Body, limit+1)); err != nil {
		return jobs.Result{}, readError(ctx, withoutURL(err))
	}
	switch {
	case int64(buf.Len()) > limit:
		return jobs.Result{}, &tooLargeError{limit}
	case buf.Len() == 0:
		return jobs.Result{}, errors.New("download the video: an empty file")
	}
	return jobs.Result{ContentType: contentType, Data: buf.Bytes()}, nil
}

// withoutURL drops the URL a client error carries, which can be a signed link.
func withoutURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	return err
}

// readError reads a body that broke off. The queue reads the context itself
// when it was the deadline or a stop; anything else was a connection that
// failed after the answer began, and asking again is safe.
func readError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("read the answer of openrouter: %w", err)
	}
	return &jobs.Error{Class: jobs.ProviderFailed, Err: fmt.Errorf("read the answer of openrouter: %w", err)}
}

// newRequest is a request to OpenRouter with the key and the headers that
// name framy.
func (p *Provider) newRequest(ctx context.Context, key, method, link string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, link, body)
	if err != nil {
		return nil, &jobs.Error{Class: jobs.Internal, Err: err}
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	if p.cfg.UserAgent != "" {
		req.Header.Set("User-Agent", p.cfg.UserAgent)
	}
	if p.cfg.Referer != "" {
		req.Header.Set("HTTP-Referer", p.cfg.Referer)
	}
	if p.cfg.Title != "" {
		req.Header.Set("X-Title", p.cfg.Title)
	}
	return req, nil
}
