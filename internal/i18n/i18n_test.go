// SPDX-License-Identifier: Apache-2.0
package i18n

import (
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"unicode"
)

// A missing string shows as "[key]" to whoever is reading the panel, so every
// key must exist in every language framy offers to pick (AGENTS.md: a key
// missing in one language is a bug).
func TestEveryKeyCoversEveryLanguage(t *testing.T) {
	for _, key := range Keys() {
		for _, code := range Codes() {
			if strings.TrimSpace(translations[key][code]) == "" {
				t.Errorf("%s is missing %s", key, code)
			}
		}
		for code := range translations[key] {
			if !IsSupported(code) {
				t.Errorf("%s has %s, which the picker does not offer", key, code)
			}
		}
	}
}

// A placeholder that survives into one language but not another renders a
// literal "{language}" to that person.
func TestPlaceholdersMatchAcrossLanguages(t *testing.T) {
	for _, key := range Keys() {
		want := placeholders(translations[key][DefaultLang])
		for _, code := range Codes() {
			if got := placeholders(translations[key][code]); got != want {
				t.Errorf("%s [%s] has placeholders %q, English has %q", key, code, got, want)
			}
		}
	}
}

func placeholders(text string) string {
	var found []string
	for {
		open := strings.IndexByte(text, '{')
		if open < 0 {
			break
		}
		end := strings.IndexByte(text[open:], '}')
		if end < 0 {
			break
		}
		found = append(found, text[open:open+end+1])
		text = text[open+end+1:]
	}
	// Order does not matter across languages, only the set does.
	sort.Strings(found)
	return strings.Join(found, ",")
}

// HTML that one language closes and another does not breaks the whole message:
// Telegram refuses it.
func TestTagsMatchAcrossLanguages(t *testing.T) {
	for _, key := range Keys() {
		want := strings.Count(translations[key][DefaultLang], "<code>")
		for _, code := range Codes() {
			s := translations[key][code]
			if strings.Count(s, "<code>") != want || strings.Count(s, "</code>") != want {
				t.Errorf("%s [%s] has unbalanced or missing <code> tags", key, code)
			}
		}
	}
}

func TestResolveFallsBackToEnglish(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"ru", "ru"},
		{"ru-RU", "ru"},
		{"uk", "uk"},
		{"be", "be"},
		{"zh_CN", "zh"},
		{"EN-gb", "en"},
		{"", "en"},
		{"kl", "en"},
		{"klingon", "en"},
	} {
		if got := Resolve(tc.in); got != tc.want {
			t.Errorf("Resolve(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The line the /f preview shows when framy cannot see a whole album: said in
// each language, with the bot's name in lowercase as the family writes it.
func TestAlbumNotAdminIsSaidInEveryLanguage(t *testing.T) {
	const english = "I see 1 photo of the album. Make framy an admin to take whole albums."
	if got := T("en", "album.not_admin"); got != english {
		t.Fatalf("English = %q", got)
	}
	for _, code := range Codes() {
		got := T(code, "album.not_admin")
		if code != "en" && got == english {
			t.Errorf("%s is not translated", code)
		}
		if n := strings.Count(got, "framy"); n != 1 || strings.Count(strings.ToLower(got), "framy") != n {
			t.Errorf("%s does not name framy once, in lowercase: %q", code, got)
		}
	}
}

// Ukrainian and Belarusian are their own languages, not Russian with another
// flag.
func TestUkrainianAndBelarusianAreNotRussian(t *testing.T) {
	for _, code := range []string{"uk", "be"} {
		if Resolve(code) != code {
			t.Fatalf("%s resolved to %q", code, Resolve(code))
		}
		for _, key := range []string{"home.keys_none", "help.body", "bot.description"} {
			if T(code, key) == T("ru", key) {
				t.Fatalf("%s reuses the Russian %s", code, key)
			}
		}
	}
}

func TestInterpolation(t *testing.T) {
	if got := T("en", "btn.group_language", "language", "Polski"); got != "Group language: Polski" {
		t.Fatalf("got %q", got)
	}
	if got := T("en", "no.such.key"); got != "[no.such.key]" {
		t.Fatalf("unknown key = %q", got)
	}
}

// Every option in the picker must be renderable, and every renderable language
// must be offered, in the family's order (core/docs/panels.md, R8).
func TestPickerIsTheFamilyList(t *testing.T) {
	want := []string{"en", "ru", "uk", "es", "fr", "de", "it", "pl", "cs", "tr", "sv", "be", "ca", "zh", "ja", "ar"}
	if got := Codes(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("picker order = %v", got)
	}
	for _, o := range LANGUAGE_OPTIONS {
		if o.Label == "" || o.Label == o.Code {
			t.Errorf("%s has no native label", o.Code)
		}
	}
}

// A name in a line of text has no flag, whatever the label in the picker has.
func TestNameOfLeavesTheFlagOut(t *testing.T) {
	for _, o := range LANGUAGE_OPTIONS {
		name := NameOf(o.Code)
		if name == "" || name == o.Label || !strings.HasSuffix(o.Label, name) {
			t.Errorf("%s: name %q from label %q", o.Code, name, o.Label)
		}
		for _, r := range name {
			if r >= 0x1F000 {
				t.Errorf("%s: %q still has a flag", o.Code, name)
			}
		}
	}
	if got := NameOf("xx"); got != "xx" {
		t.Errorf("an unknown code is its own name, got %q", got)
	}
}

// Telegram caps a bot's short description at 120 characters, its description
// at 512 and a command's at 256; longer ones are refused.
func TestTelegramLimits(t *testing.T) {
	for key, limit := range map[string]int{"bot.short": 120, "bot.description": 512, "cmd.start": 256, "cmd.start_group": 256} {
		for _, code := range Codes() {
			if n := len([]rune(T(code, key))); n > limit {
				t.Errorf("%s [%s] is %d characters, over %d", key, code, n, limit)
			}
		}
	}
}

// R9: a panel carries no emoji. Flags belong to the picker's labels, which
// are not translations.
func TestNoEmojiInTranslations(t *testing.T) {
	for _, key := range Keys() {
		for _, code := range Codes() {
			for _, r := range translations[key][code] {
				if isEmoji(r) {
					t.Errorf("%s [%s] has an emoji %q", key, code, r)
				}
			}
		}
	}
}

func isEmoji(r rune) bool {
	return unicode.Is(unicode.So, r) && r > 0x2000 ||
		(r >= 0x1F000 && r <= 0x1FAFF) || r == 0xFE0F
}

// The em dash stands in for a colon, a comma or a full stop in machine-made
// text and reads as filler, so nothing a person reads in framy has one.
const emDash = "—"

func TestNoEmDashInTranslations(t *testing.T) {
	for _, key := range Keys() {
		for _, code := range Codes() {
			if s := translations[key][code]; strings.Contains(s, emDash) {
				t.Errorf("%s [%s] has an em dash: %q", key, code, s)
			}
		}
	}
}

func TestNoEmDashInGoStrings(t *testing.T) {
	for _, dir := range []string{"../../internal", "../../cmd"} {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			fset := token.NewFileSet()
			file := fset.AddFile(path, -1, len(src))
			var s scanner.Scanner
			s.Init(file, src, nil, 0)
			for {
				pos, tok, lit := s.Scan()
				if tok == token.EOF {
					break
				}
				if (tok == token.STRING || tok == token.CHAR) && strings.Contains(lit, emDash) {
					t.Errorf("%s: a string has an em dash: %s", fset.Position(pos), lit)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
