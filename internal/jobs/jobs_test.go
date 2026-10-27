// SPDX-License-Identifier: Apache-2.0
package jobs

import (
	"strings"
	"testing"
)

func TestRedactTakesPresignedLinksOut(t *testing.T) {
	link := "https://acc.r2.cloudflarestorage.com/framy/u/1/in/a.jpg?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=key%2F20261027%2Fauto%2Fs3%2Faws4_request&X-Amz-Date=20261027T100000Z&X-Amz-Expires=900&X-Amz-SignedHeaders=host&X-Amz-Signature=0123abcd"
	for _, msg := range []string{
		"refused: could not fetch " + link,
		`provider: {"error":{"message":"bad image at ` + link + `"}}`,
		"provider: input_references[0]: " + strings.ReplaceAll(link, "&", `&`) + " is not an image",
	} {
		got := redact(msg)
		if strings.Contains(got, "X-Amz-Signature") || strings.Contains(got, "X-Amz-Credential") || !strings.Contains(got, "<presigned link>") {
			t.Errorf("redact(%q) = %q", msg, got)
		}
	}
	if got := redact("rate_limited: 429 from https://openrouter.ai/api/v1/images"); got != "rate_limited: 429 from https://openrouter.ai/api/v1/images" {
		t.Errorf("an ordinary link was taken: %q", got)
	}
}
