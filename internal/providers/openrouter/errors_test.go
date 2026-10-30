// SPDX-License-Identifier: Apache-2.0
package openrouter

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/FreshLabDev/framy/internal/jobs"
)

// passed is OpenRouter's 400 that passes on the answer of the provider
// behind it, as BytePlus gave it for Seedance on 30 October 2026.
func passed(code, words string) []byte {
	inner, _ := json.Marshal(map[string]any{"error": map[string]any{
		"code": code, "message": words, "param": "content[1]", "type": "BadRequest",
	}})
	body, _ := json.Marshal(map[string]any{"error": map[string]any{
		"code": 400, "message": "HTTP 400: " + string(inner), "metadata": map[string]any{"provider_name": "BytePlus"},
	}})
	return body
}

// A provider that refuses a real person in a clip or a picture fails the
// run as faces, and the person is shown its words alone: without framy's
// wrapping, the JSON OpenRouter passed on and the request id. The logs keep
// the code.
func TestARefusedPersonIsFaces(t *testing.T) {
	for _, tc := range []struct{ code, words, said string }{
		{"InputVideoSensitiveContentDetected.PrivacyInformation",
			"The request failed because the input video 'content[1]' may contain real person. Request id: 0217618342519b7e5f4",
			"The request failed because the input video 'content[1]' may contain real person."},
		{"InputImageSensitiveContentDetected.PrivacyInformation",
			"The request failed because the input image may contain real person. Request id: 0217-abc",
			"The request failed because the input image may contain real person."},
	} {
		pe := statusError(http.StatusBadRequest, nil, passed(tc.code, tc.words))
		if pe.Class != jobs.Faces || pe.Said() != tc.said || !strings.Contains(pe.Error(), tc.code) || strings.Contains(pe.Error(), "Request id") {
			t.Errorf("%s: class %s, said %q, error %q", tc.code, pe.Class, pe.Said(), pe.Error())
		}
	}
}

// Any other answer passed on stays refused, with the provider's words; a
// message that is not one keeps its words; an answer with no error of
// OpenRouter's says nothing of the provider's.
func TestTheProvidersWordsAreKeptClean(t *testing.T) {
	pe := statusError(http.StatusBadRequest, nil, passed("InvalidParameter.Duration", "The parameter duration is invalid"))
	var api *APIError
	if pe.Class != jobs.Refused || pe.Said() != "The parameter duration is invalid." || !errors.As(pe, &api) || api.Code != "InvalidParameter.Duration" {
		t.Errorf("class %s, said %q", pe.Class, pe.Said())
	}

	pe = statusError(http.StatusBadRequest, nil, []byte(`{"error":{"code":400,"message":"HTTP 400: not json at all"}}`))
	if pe.Class != jobs.Refused || pe.Said() != "HTTP 400: not json at all" {
		t.Errorf("class %s, said %q", pe.Class, pe.Said())
	}

	pe = statusError(524, nil, []byte(`<html>A timeout occurred</html>`))
	if pe.Said() != "" || pe.Error() != "timeout: openrouter 524" {
		t.Errorf("said %q, error %q", pe.Said(), pe.Error())
	}
}
