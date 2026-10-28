// SPDX-License-Identifier: Apache-2.0
package gemini

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/jobs"
)

// interaction is the answer of POST /interactions and GET
// /interactions/{id}, in the schema of the 2026-05-20 revision: the result is
// a list of steps, and the image or the video sits in a model_output step.
// Thought steps carry the model's interim drawings, which Google does not
// bill, and are not read.
type interaction struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Steps  []step `json:"steps"`
	Usage  *usage `json:"usage"`
	// Error and Errors: a run that failed after Google took it.
	Error  *errorBody  `json:"error"`
	Errors []errorBody `json:"errors"`
}

// step is one step of an interaction. Arguments and Result are read only for
// the search steps: other kinds of step use the same names for other shapes.
type step struct {
	Type      string          `json:"type"`
	Content   []block         `json:"content"`
	Arguments json.RawMessage `json:"arguments"`
	Result    json.RawMessage `json:"result"`
}

type block struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	MimeType string `json:"mime_type"`
	Data     string `json:"data"`
	URI      string `json:"uri"`
	// Annotations of a text: the pages a grounded answer cites, among
	// others.
	Annotations []json.RawMessage `json:"annotations"`
}

// usage is what a run consumed. Output includes the image's or the video's
// tokens; thoughts and the tokens of a search are counted apart.
type usage struct {
	TotalInput   int64 `json:"total_input_tokens"`
	TotalOutput  int64 `json:"total_output_tokens"`
	TotalThought int64 `json:"total_thought_tokens"`
	TotalTool    int64 `json:"total_tool_use_tokens"`
	InputBy      []by  `json:"input_tokens_by_modality"`
	OutputBy     []by  `json:"output_tokens_by_modality"`
}

type by struct {
	Modality string `json:"modality"`
	Tokens   int64  `json:"tokens"`
}

// Billable lines of the catalog, as OpenRouter names them: the direction and
// the modality, input_video and the like.
const (
	inputText  = "input_text"
	outputText = "output_text"
)

// cost prices the run: the tokens times the descriptor's rates, by modality
// (input_image, output_video). Thinking tokens are billed at the output text
// rate, a search's tokens at the input text rate. Tokens of a modality the
// descriptor has no rate for, or that the answer does not split by modality,
// are priced at the dearest rate of their direction, so the cost is never
// below what was spent. It is nil when the answer has no usage or the
// descriptor has no rate at all for tokens that were used: a number that is
// wrong is worse than none.
//
// Search queries beyond Google's free 5,000 a month are billed apart, per
// query, and are not in the cost: framy cannot tell how many of the free ones
// the person's project has left.
func cost(prices []catalog.Price, u *usage) *float64 {
	if u == nil {
		return nil
	}
	rate := map[string]float64{}
	for _, p := range prices {
		if p.Unit == "token" {
			rate[p.Billable] = p.CostUSD
		}
	}
	var usd float64
	ok := true
	charge := func(tokens int64, billable string) {
		if tokens == 0 {
			return
		}
		r, found := rate[billable]
		if !found {
			ok = false
		}
		usd += float64(tokens) * r
	}
	// dearest is the highest rate of a direction, the first by name of equal
	// ones; "" when the direction has none.
	dearest := func(direction string) string {
		best := ""
		for _, b := range slices.Sorted(maps.Keys(rate)) {
			if strings.HasPrefix(b, direction+"_") && (best == "" || rate[b] > rate[best]) {
				best = b
			}
		}
		return best
	}
	byModality := func(direction string, total int64, parts []by) {
		if len(parts) == 0 {
			charge(total, dearest(direction))
			return
		}
		for _, p := range parts {
			b := direction + "_" + strings.ToLower(p.Modality)
			if _, found := rate[b]; !found {
				b = dearest(direction)
			}
			charge(p.Tokens, b)
		}
	}
	byModality("input", u.TotalInput, u.InputBy)
	byModality("output", u.TotalOutput, u.OutputBy)
	charge(u.TotalThought, outputText)
	charge(u.TotalTool, inputText)
	if !ok {
		return nil
	}
	return &usd
}

// pricesOf are the rates of every route of the offer.
func pricesOf(of catalog.Offer) []catalog.Price {
	var prices []catalog.Price
	for _, route := range of.Routes {
		prices = append(prices, route.Prices...)
	}
	return prices
}

// made is what a finished interaction holds: the files of the kind asked
// for, the model's words and what a search brought.
type made struct {
	media     []block
	text      string
	grounding *jobs.Grounding
}

// collect reads the steps of an interaction. kind is "image" or "video".
func collect(r interaction, kind string) made {
	var m made
	var texts []string
	g := &jobs.Grounding{}
	for _, s := range r.Steps {
		switch s.Type {
		case "model_output":
			for _, b := range s.Content {
				switch b.Type {
				case kind:
					m.media = append(m.media, b)
				case "text":
					if t := strings.TrimSpace(b.Text); t != "" {
						texts = append(texts, t)
					}
					g.Sources = appendSources(g.Sources, b.Annotations)
				}
			}
		case "google_search_call":
			var args struct {
				Queries []string `json:"queries"`
			}
			if json.Unmarshal(s.Arguments, &args) == nil {
				for _, q := range args.Queries {
					if q = strings.TrimSpace(q); q != "" && len(g.Queries) < maxQueries && !slices.Contains(g.Queries, q) {
						g.Queries = append(g.Queries, clipTo(q, maxQuery))
					}
				}
			}
		case "google_search_result":
			var results []struct {
				SearchSuggestions string `json:"search_suggestions"`
			}
			if json.Unmarshal(s.Result, &results) == nil {
				for _, res := range results {
					if html := strings.TrimSpace(res.SearchSuggestions); html != "" && len(html) <= maxSuggestion &&
						len(g.Suggestions) < maxSuggestions && !slices.Contains(g.Suggestions, html) {
						g.Suggestions = append(g.Suggestions, html)
					}
				}
			}
		}
	}
	m.text = clipText(strings.Join(texts, "\n\n"))
	if !g.Empty() {
		m.grounding = g
	}
	return m
}

// Bounds on what a grounded answer brings, so a row stays small.
const (
	maxSuggestions = 4
	// maxSuggestion is one suggestions snippet: Google's are a few
	// kilobytes of HTML and CSS. A larger one is dropped, not cut, since cut
	// HTML is broken.
	maxSuggestion = 64 << 10
	maxQueries    = 16
	maxQuery      = 300
	maxSources    = 32
	maxURL        = 2048
)

// appendSources adds the pages a text's url_citation annotations cite, each
// once.
func appendSources(sources []jobs.Source, annotations []json.RawMessage) []jobs.Source {
	for _, raw := range annotations {
		var a struct {
			Type  string `json:"type"`
			URL   string `json:"url"`
			Title string `json:"title"`
		}
		if json.Unmarshal(raw, &a) != nil || a.Type != "url_citation" || len(sources) >= maxSources {
			continue
		}
		u, err := url.Parse(a.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || len(a.URL) > maxURL {
			continue
		}
		if slices.ContainsFunc(sources, func(s jobs.Source) bool { return s.URL == a.URL }) {
			continue
		}
		sources = append(sources, jobs.Source{URL: a.URL, Title: clipTo(strings.TrimSpace(a.Title), maxQuery)})
	}
	return sources
}

// clipTo keeps s to n bytes, on a rune boundary.
func clipTo(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// outcome reads the 200 of an image run. raw is the body, read again only to
// find a refusal when no image came back.
func outcome(of catalog.Offer, raw []byte, key string) jobs.Outcome {
	var r interaction
	if err := json.Unmarshal(raw, &r); err != nil {
		// The run was likely paid, so this is not tried again.
		return jobs.Outcome{Err: &jobs.Error{Class: jobs.ProviderFailed, Err: fmt.Errorf("read the answer of gemini: %w", err)}}
	}
	out := jobs.Outcome{CostUSD: cost(pricesOf(of), r.Usage)}
	m := collect(r, "image")
	if len(m.media) == 0 {
		out.Err = noResult(r, raw, m.text, key, catalog.Image)
		return out
	}
	if !trustworthy(r.Usage) {
		out.CostUSD = nil
	}
	for i, b := range m.media {
		res, err := result(b)
		if err != nil {
			out.Results = nil
			out.Err = &jobs.Error{Class: jobs.ProviderFailed, Err: fmt.Errorf("image %d of the answer: %w", i+1, err)}
			return out
		}
		out.Results = append(out.Results, res)
	}
	out.Grounding = m.grounding
	return out
}

// trustworthy: a result with no output tokens means the usage is not to be
// trusted; better no cost than one far too low.
func trustworthy(u *usage) bool {
	return u != nil && (u.TotalOutput+u.TotalThought > 0 || len(u.OutputBy) > 0)
}

// result decodes one image of the answer.
func result(b block) (jobs.Result, error) {
	if b.Data == "" {
		if b.URI != "" {
			// A Files API link needs the key to open; the sink has none.
			return jobs.Result{}, errors.New("the image came as a link, not as data")
		}
		return jobs.Result{}, errors.New("an image with no data")
	}
	data, err := base64.StdEncoding.DecodeString(b.Data)
	if err != nil {
		return jobs.Result{}, fmt.Errorf("decode base64: %w", err)
	}
	if len(data) == 0 {
		return jobs.Result{}, errors.New("an empty image")
	}
	ct := b.MimeType
	if !strings.HasPrefix(ct, "image/") {
		ct = http.DetectContentType(data)
	}
	return jobs.Result{ContentType: ct, Data: data}, nil
}

// noResult says why a finished run holds no image or video: a refusal when
// Google blocked it or the model answered in words, a failure otherwise.
func noResult(r interaction, raw []byte, text, key string, kind catalog.Kind) *jobs.Error {
	none, what, noun := ReasonNoImage, "an image", "image"
	if kind == catalog.Video {
		none, what, noun = ReasonNoVideo, "a video", "video"
	}
	// An error object Google put in the body.
	if r.Error != nil {
		return withText(bodyError(r.Error, key), text)
	}
	var first *jobs.Error
	for i := range r.Errors {
		pe := bodyError(&r.Errors[i], key)
		if pe.Class == jobs.Moderation {
			return withText(pe, text)
		}
		if first == nil {
			first = pe
		}
	}
	if first != nil {
		return first
	}
	// blockReason and finishReason, wherever the answer puts them.
	found := reasonsIn(raw)
	switch {
	case found.block != "":
		return refusal(found.block, stagePrompt, "", found.blockMessage)
	case found.finish != "":
		if !declined[found.finish] {
			return &jobs.Error{Class: jobs.ProviderFailed, Err: fmt.Errorf("gemini ended the run without %s (%s)%s", what, found.finish, suffix(found.finishMessage))}
		}
		if found.finish == ReasonNoImage || found.finish == ReasonNoVideo {
			return refusal(found.finish, stageResult, cmp.Or(text, found.finishMessage), "")
		}
		return refusal(found.finish, stageResult, "", cmp.Or(found.finishMessage, text))
	}
	switch r.Status {
	case statusFailed, statusCancelled:
		return &jobs.Error{Class: jobs.ProviderFailed, Err: fmt.Errorf("gemini ended the run as %s with no %s", r.Status, noun)}
	case statusIncomplete, statusInProgress, statusRequiresAction, statusRequiresContinuation:
		return &jobs.Error{Class: jobs.ProviderFailed, Err: fmt.Errorf("gemini returned the run as %s with no %s", r.Status, noun)}
	}
	if text != "" {
		// A finished run with words and no picture: the model declined to
		// make one, or took the prompt for a question.
		return refusal(none, stageResult, text, "")
	}
	return &jobs.Error{Class: jobs.ProviderFailed, Err: fmt.Errorf("gemini returned neither %s nor text", what)}
}

// withText adds the model's words to a refusal that came without them.
func withText(pe *jobs.Error, text string) *jobs.Error {
	var r *Refusal
	if errors.As(pe, &r) && r.Text == "" && (r.Reason == ReasonNoImage || r.Reason == ReasonNoVideo) {
		r.Text = text
	}
	return pe
}

func suffix(s string) string {
	if s == "" {
		return ""
	}
	return ": " + s
}

func refusal(reason, stage, text, message string) *jobs.Error {
	return &jobs.Error{Class: jobs.Moderation, Err: &Refusal{
		Reason: reason, Stage: stage, Text: clipText(text), Message: clip(strings.TrimSpace(message)),
	}}
}

// reasons are the blockReason and the finishReason of an answer, with their
// messages. Google's own words are upper case; absent, unspecified and STOP
// are not reasons.
type reasons struct {
	block, blockMessage   string
	finish, finishMessage string
}

// reasonsIn looks for the fields under any key spelt blockReason or
// block_reason, finishReason or finish_reason, at any depth: the Interactions
// API documents its failures as error codes, and the older candidates form
// puts these fields in promptFeedback and in each candidate. Reading both
// keeps a refusal from being taken for a failure whichever the answer uses.
func reasonsIn(raw []byte) reasons {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	if dec.Decode(&v) != nil {
		return reasons{}
	}
	var r reasons
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for _, k := range slices.Sorted(maps.Keys(x)) {
				val := x[k]
				s, isString := val.(string)
				switch strings.ReplaceAll(strings.ToLower(k), "_", "") {
				case "blockreason":
					if s = strings.ToUpper(s); isString && s != "" && s != "BLOCK_REASON_UNSPECIFIED" && r.block == "" {
						r.block = s
					}
				case "blockreasonmessage":
					if isString && r.blockMessage == "" {
						r.blockMessage = s
					}
				case "finishreason":
					if s = strings.ToUpper(s); isString && s != "" && s != "STOP" && s != "FINISH_REASON_UNSPECIFIED" && r.finish == "" {
						r.finish = s
					}
				case "finishmessage":
					if isString && r.finishMessage == "" {
						r.finishMessage = s
					}
				case "data":
					// A picture or a video in base64: nothing to find there.
				default:
					walk(val)
				}
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(v)
	return r
}
