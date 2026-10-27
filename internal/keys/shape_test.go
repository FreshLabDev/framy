// SPDX-License-Identifier: Apache-2.0
package keys

import (
	"strings"
	"testing"
)

func TestLooksLikeKey(t *testing.T) {
	openAI := "sk-proj-" + strings.Repeat("aB3xY9", 12)
	openAIOld := "sk-" + strings.Repeat("Zq81nT3v", 6)
	openRouter := "sk-or-v1-" + strings.Repeat("0123456789abcdef", 4)
	gemini := "AIza" + strings.Repeat("Xy7_-Qw", 5)
	for name, text := range map[string]string{
		"an OpenAI project key":  openAI,
		"an old OpenAI key":      openAIOld,
		"an OpenRouter key":      openRouter,
		"a Gemini key":           gemini,
		"in a sentence":          "my key is " + openAI + " ok",
		"after a colon":          "key:" + gemini,
		"in quotes":              `"` + openRouter + `"`,
		"in a code fence":        "```\n" + openAI + "\n```",
		"a service account key":  "sk-svcacct-" + strings.Repeat("Ab1_", 10),
		"with a trailing period": openRouter + ".",
		"after a newline":        "hello\n" + gemini,
		"as an env line":         "OPENAI_API_KEY=" + openAI,
	} {
		if !LooksLikeKey(text) {
			t.Errorf("%s: not taken for a key", name)
		}
	}
	if !LooksLikeKey("nothing here", "caption: "+gemini) {
		t.Error("the second text was not read")
	}
	for name, text := range map[string]string{
		"nothing":              "",
		"prose":                "a red fox in the snow, soft light",
		"a short sk-":          "sk-1234",
		"a short AIza":         "AIza1234567",
		"a word ending in sk-": "task-" + strings.Repeat("a1", 12),
		"a path":               "/var/task-" + strings.Repeat("Ab1", 10),
		"lowercase prose":      "use sk-learn-pipeline-example for it",
		"a hyphenated phrase":  "a-sk-" + strings.Repeat("ab", 12),
		"AIza in the middle":   "xAIza" + strings.Repeat("a1", 20),
		"a Cyrillic sentence":  "нарисуй лису на снегу, мягкий свет",
		"a long URL":           "https://example.com/" + strings.Repeat("abc-", 20),
	} {
		if LooksLikeKey(text) {
			t.Errorf("%s: taken for a key", name)
		}
	}
}
