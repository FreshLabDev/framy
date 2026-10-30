// SPDX-License-Identifier: Apache-2.0
package openrouter

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/jobs"
)

// reserved are the fields framy fills itself; no setting may set them.
var reserved = map[string]bool{
	"model": true, "prompt": true, "provider": true, "input_references": true,
	"user": true, "stream": true, "session_id": true, "trace": true,
}

// Kinds of values the Images API takes.
type valueKind int

const (
	anyKind valueKind = iota
	integer
	text
)

// kinds are the request fields whose value is not what the catalog's type
// says: OpenRouter lists seed and size as boolean, meaning only that the
// route takes them, while the value is a number or a size such as
// "1024x1536". n and output_compression are ranges of whole numbers, and a
// video's duration is a whole number of seconds.
var kinds = map[string]valueKind{
	"seed":               integer,
	"size":               text,
	"n":                  integer,
	"output_compression": integer,
	"duration":           integer,
}

// referencesParam is how a route says how many reference images it takes.
const referencesParam = "input_references"

// imageRequest builds the body of POST /images. What framy can tell is wrong
// before the run comes back as refused, so nothing is paid for it:
//
//   - the settings go as fields of the request, or among the route's own
//     options when the catalog marks them pass-through;
//   - the request goes only to routes that take every setting and the number
//     of references; OpenRouter tries the cheapest first, as the estimate
//     assumes, and may fall back to another of them.
func imageRequest(j jobs.Job) (map[string]any, error) {
	of := j.Offer
	if of.ProviderModelID == "" {
		return nil, refused("the offer %s has no model id", j.OfferID)
	}
	if strings.TrimSpace(j.Prompt) == "" {
		return nil, refused("the prompt is empty")
	}
	var refs []map[string]any
	for i, in := range j.Inputs {
		switch {
		case !strings.HasPrefix(in.ContentType, "image/"):
			return nil, refused("input %d is %q, and OpenRouter's Images API takes only images", i+1, in.ContentType)
		case in.URL == "":
			return nil, &jobs.Error{Class: jobs.Storage, Err: fmt.Errorf("input %d has no link", i+1)}
		}
		refs = append(refs, map[string]any{"type": "image_url", "image_url": map[string]any{"url": in.URL}})
	}
	settings := map[string]any{}
	for name, v := range j.Settings {
		if v == nil {
			continue
		}
		if reserved[name] {
			return nil, refused("%s is not a setting", name)
		}
		settings[name] = normalize(name, v)
	}
	routes, err := routesFor(of, settings, len(refs))
	if err != nil {
		return nil, err
	}

	body := map[string]any{"model": of.ProviderModelID, "prompt": j.Prompt}
	options := map[string]map[string]any{}
	for _, name := range slices.Sorted(maps.Keys(settings)) {
		if !passthrough(of, name) {
			body[name] = settings[name]
			continue
		}
		for _, r := range routes {
			if options[r.Provider] == nil {
				options[r.Provider] = map[string]any{}
			}
			options[r.Provider][name] = settings[name]
		}
	}
	if len(refs) > 0 {
		body[referencesParam] = refs
	}
	prefs := map[string]any{"sort": "price"}
	if len(routes) < len(of.Routes) {
		only := make([]string, len(routes))
		for i, r := range routes {
			only[i] = r.Provider
		}
		prefs["only"] = only
	}
	if len(options) > 0 {
		prefs["options"] = options
	}
	body["provider"] = prefs
	body["user"] = jobs.SafetyID(j.UserID)
	return body, nil
}

// routesFor keeps the routes that take the run as it is.
func routesFor(of catalog.Offer, settings map[string]any, refs int) ([]catalog.Route, error) {
	if len(of.Routes) == 0 {
		return nil, refused("OpenRouter lists no route for %s", of.ID)
	}
	var ok []catalog.Route
	var why []string
	for _, r := range of.Routes {
		if r.Provider == "" {
			why = append(why, "a route with no provider")
			continue
		}
		if reason := takes(r, settings, refs); reason != "" {
			why = append(why, r.Provider+": "+reason)
			continue
		}
		ok = append(ok, r)
	}
	if len(ok) == 0 {
		return nil, refused("no route of %s takes this run: %s", of.ID, strings.Join(why, "; "))
	}
	return ok, nil
}

// takes says why route r cannot take the run, or "".
func takes(r catalog.Route, settings map[string]any, refs int) string {
	for _, name := range slices.Sorted(maps.Keys(settings)) {
		p, ok := r.Params[name]
		if !ok {
			return name + " is not a setting here"
		}
		if !accepts(name, p, settings[name]) {
			return fmt.Sprintf("%s=%v is not allowed", name, settings[name])
		}
	}
	p, listed := r.Params[referencesParam]
	switch {
	case refs == 0 && listed && p.Min != nil && *p.Min > 0:
		return "it needs a reference image"
	case refs > 0 && !listed:
		return "it takes no reference images"
	case refs > 0 && p.Max != nil && float64(refs) > *p.Max:
		return fmt.Sprintf("it takes at most %v reference images, not %d", *p.Max, refs)
	}
	return ""
}

// accepts says whether v is a value of p.
func accepts(name string, p catalog.Param, v any) bool {
	switch p.Type {
	case catalog.Enum:
		return slices.ContainsFunc(p.Values, func(x any) bool { return same(x, v) })
	case catalog.Range:
		f, ok := number(v)
		if !ok || (p.Min != nil && f < *p.Min) || (p.Max != nil && f > *p.Max) {
			return false
		}
		return kinds[name] != integer || f == math.Trunc(f)
	case catalog.Boolean:
		switch kinds[name] {
		case integer:
			f, ok := number(v)
			return ok && f == math.Trunc(f)
		case text:
			s, ok := v.(string)
			return ok && s != ""
		}
		_, ok := v.(bool)
		return ok
	case catalog.Text:
		_, ok := v.(string)
		return ok
	}
	return false
}

// passthrough: the catalog marks the setting as one of the routes' own.
func passthrough(of catalog.Offer, name string) bool {
	if p, ok := of.Params[name]; ok {
		return p.Passthrough
	}
	for _, r := range of.Routes {
		if p, ok := r.Params[name]; ok && p.Passthrough {
			return true
		}
	}
	return false
}

// normalize writes a whole number as one, so 2 never goes as 2.0.
func normalize(name string, v any) any {
	if kinds[name] != integer {
		return v
	}
	if f, ok := number(v); ok && f == math.Trunc(f) && math.Abs(f) < 1<<53 {
		return int64(f)
	}
	return v
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// same compares a catalog value with a setting as JSON, so 1 and 1.0 match.
func same(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ja) == string(jb)
}

// refused is a run the adapter does not send, since it would fail: framy's
// own words, before anything reaches the provider.
func refused(format string, args ...any) *jobs.Error {
	return &jobs.Error{Class: jobs.Invalid, Err: fmt.Errorf(format, args...)}
}
