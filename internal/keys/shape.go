// SPDX-License-Identifier: Apache-2.0
package keys

import (
	"regexp"
	"strings"
)

// keyLike finds a token shaped like a provider key: OpenAI's sk-proj-…,
// OpenRouter's sk-or-v1-…, any other sk-… and Gemini's AIza…. A key does not
// start in the middle of a word, so "task-…" is not one.
var keyLike = regexp.MustCompile(`(?:^|[^A-Za-z0-9_-])(sk-[A-Za-z0-9_-]{16,}|AIza[A-Za-z0-9_-]{30,})`)

// LooksLikeKey reports whether any of the texts holds something shaped like a
// provider key. The bot deletes such a message at once. A real key is random,
// so a long run of lowercase words and dashes, such as "sk-learn-pipeline-
// example", is not taken for one: the part after the prefix needs a digit or
// a capital.
func LooksLikeKey(texts ...string) bool {
	for _, text := range texts {
		for _, m := range keyLike.FindAllStringSubmatch(text, -1) {
			body, ok := strings.CutPrefix(m[1], "sk-")
			if !ok {
				body = strings.TrimPrefix(m[1], "AIza")
			}
			if strings.IndexFunc(body, func(r rune) bool { return r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' }) >= 0 {
				return true
			}
		}
	}
	return false
}
