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
	"slices"
	"strings"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/jobs"
)

// interaction is the answer of POST /interactions, in the schema of the
// 2026-05-20 revision: the result is a list of steps, and the image sits in a
// model_output step. Thought steps carry the model's interim drawings, which
// Google does not bill, and are not read.
type interaction struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Steps  []struct {
		Type    string  `json:"type"`
		Content []block `json:"content"`
	} `json:"steps"`
	Usage *usage `json:"usage"`
	// Error and Errors: a run that failed after Google took it.
	Error  *errorBody  `json:"error"`
	Errors []errorBody `json:"errors"`
}

type block struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	MimeType string `json:"mime_type"`
	Data     string `json:"data"`
	URI      string `json:"uri"`
}

// usage is what a run consumed. Output includes the image's tokens; thoughts
// and the tokens of a search are counted apart.
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

// Billable lines of the catalog, as OpenRouter names them.
const (
	inputText   = "input_text"
	inputImage  = "input_image"
	outputText  = "output_text"
	outputImage = "output_image"
)

// split adds up tokens by modality: text, image, and anything else (audio,
// video, documents) or the whole total when the answer gives no split.
func split(total int64, parts []by) (text, image, other int64) {
	if len(parts) == 0 {
		return 0, 0, total
	}
	for _, p := range parts {
		switch strings.ToLower(p.Modality) {
		case "text":
			text += p.Tokens
		case "image":
			image += p.Tokens
		default:
			other += p.Tokens
		}
	}
	return text, image, other
}

// cost prices the run: the tokens times the descriptor's rates. Text and
// thinking tokens are billed at the output text rate, a search's tokens at
// the input rate. Tokens the answer does not split by modality are priced at
// the dearer of the two rates, so the cost is never below what was spent.
// It is nil when the answer has no usage or the descriptor lacks a rate for
// tokens that were used: a number that is wrong is worse than none.
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
	dearer := func(a, b string) string {
		if rate[a] >= rate[b] {
			return a
		}
		return b
	}

	text, image, other := split(u.TotalInput, u.InputBy)
	charge(text, inputText)
	charge(image, inputImage)
	charge(other, dearer(inputText, inputImage))

	text, image, other = split(u.TotalOutput, u.OutputBy)
	charge(text, outputText)
	charge(image, outputImage)
	charge(other, dearer(outputText, outputImage))

	charge(u.TotalThought, outputText)
	charge(u.TotalTool, inputText)
	if !ok {
		return nil
	}
	return &usd
}

// outcome reads a 200. raw is the body, read again only to find a refusal
// when no image came back.
func outcome(of catalog.Offer, raw []byte, key string) jobs.Outcome {
	var r interaction
	if err := json.Unmarshal(raw, &r); err != nil {
		// The run was likely paid, so this is not tried again.
		return jobs.Outcome{Err: &jobs.Error{Class: jobs.ProviderFailed, Err: fmt.Errorf("read the answer of gemini: %w", err)}}
	}
	var prices []catalog.Price
	for _, route := range of.Routes {
		prices = append(prices, route.Prices...)
	}
	out := jobs.Outcome{CostUSD: cost(prices, r.Usage)}

	var texts []string
	var images []block
	for _, s := range r.Steps {
		if s.Type != "model_output" {
			continue
		}
		for _, b := range s.Content {
			switch b.Type {
			case "image":
				images = append(images, b)
			case "text":
				if t := strings.TrimSpace(b.Text); t != "" {
					texts = append(texts, t)
				}
			}
		}
	}
	text := clipText(strings.Join(texts, "\n\n"))

	if len(images) > 0 {
		if r.Usage == nil || (r.Usage.TotalOutput+r.Usage.TotalThought == 0 && len(r.Usage.OutputBy) == 0) {
			// An image with no output tokens means the usage is not to be
			// trusted; better no cost than one far too low.
			out.CostUSD = nil
		}
		for i, b := range images {
			res, err := result(b)
			if err != nil {
				out.Results = nil
				out.Err = &jobs.Error{Class: jobs.ProviderFailed, Err: fmt.Errorf("image %d of the answer: %w", i+1, err)}
				return out
			}
			out.Results = append(out.Results, res)
		}
		return out
	}
	out.Err = noImage(r, raw, text, key)
	return out
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

// noImage says why a finished run holds no image: a refusal when Google
// blocked it or the model answered in words, a failure otherwise.
func noImage(r interaction, raw []byte, text, key string) *jobs.Error {
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
			return &jobs.Error{Class: jobs.ProviderFailed, Err: fmt.Errorf("gemini ended the run without an image (%s)%s", found.finish, suffix(found.finishMessage))}
		}
		if found.finish == ReasonNoImage {
			return refusal(ReasonNoImage, stageResult, cmp.Or(text, found.finishMessage), "")
		}
		return refusal(found.finish, stageResult, "", cmp.Or(found.finishMessage, text))
	}
	switch r.Status {
	case "failed", "cancelled":
		return &jobs.Error{Class: jobs.ProviderFailed, Err: fmt.Errorf("gemini ended the run as %s with no image", r.Status)}
	case "incomplete", "in_progress", "requires_action":
		return &jobs.Error{Class: jobs.ProviderFailed, Err: fmt.Errorf("gemini returned the run as %s with no image", r.Status)}
	}
	if text != "" {
		// A finished run with words and no picture: the model declined to
		// draw, or took the prompt for a question.
		return refusal(ReasonNoImage, stageResult, text, "")
	}
	return &jobs.Error{Class: jobs.ProviderFailed, Err: errors.New("gemini returned neither an image nor text")}
}

// withText adds the model's words to a refusal that came without them.
func withText(pe *jobs.Error, text string) *jobs.Error {
	var r *Refusal
	if errors.As(pe, &r) && r.Text == "" && r.Reason == ReasonNoImage {
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
