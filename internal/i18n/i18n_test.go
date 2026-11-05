// SPDX-License-Identifier: Apache-2.0
package i18n

import (
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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
// Telegram refuses it. Every tag, entity and bare "<", ">" or "&" of a
// language is English's, and its tags close in order.
func TestMarkupMatchesAcrossLanguages(t *testing.T) {
	for _, key := range Keys() {
		want := markupOf(translations[key][DefaultLang])
		for _, code := range Codes() {
			s := translations[key][code]
			if got := markupOf(s); got != want {
				t.Errorf("%s [%s] has markup %q, English has %q", key, code, got, want)
			}
			if !balanced(s) {
				t.Errorf("%s [%s] does not close its tags in order: %q", key, code, s)
			}
		}
	}
}

var markup = regexp.MustCompile(`<[^<>]*>|&[#a-zA-Z0-9]+;|[<>&]`)

func markupOf(s string) string {
	found := markup.FindAllString(s, -1)
	sort.Strings(found)
	return strings.Join(found, " ")
}

// balanced tells whether every tag of s closes, innermost first.
func balanced(s string) bool {
	var open []string
	for _, tag := range markup.FindAllString(s, -1) {
		if !strings.HasPrefix(tag, "<") || len(tag) < 3 || strings.HasSuffix(tag, "/>") {
			continue
		}
		name, _, _ := strings.Cut(strings.Trim(tag, "</>"), " ")
		if tag[1] != '/' {
			open = append(open, name)
			continue
		}
		if len(open) == 0 || open[len(open)-1] != name {
			return false
		}
		open = open[:len(open)-1]
	}
	return len(open) == 0
}

func TestBalancedReadsTagsInOrder(t *testing.T) {
	for s, want := range map[string]bool{
		"<code>/img</code> and <b><i>x</i></b>": true,
		`<a href="https://t.me">a</a>`:          true,
		"<code>/img":                            false,
		"</code>/img<code>":                     false,
		"<b><i>x</b></i>":                       false,
	} {
		if got := balanced(s); got != want {
			t.Errorf("balanced(%q) = %v", s, got)
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

// The line the /img preview shows when framy cannot see a whole album: said in
// each language, with the bot's name in lowercase as the family writes it.
func TestAlbumNotAdminIsSaidInEveryLanguage(t *testing.T) {
	const english = "Only 1 photo of the album is taken. Make framy an admin to take whole albums."
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

// A straight ' or " is a typewriter's mark. framy writes the typographic ones
// (’ “ ” « » „), so the bot and the site set "framy’s" alike; the only markup
// in a translation is <code>, which takes no attribute to quote.
func TestNoStraightQuotesInTranslations(t *testing.T) {
	for _, key := range Keys() {
		for _, code := range Codes() {
			if s := translations[key][code]; strings.ContainsAny(s, `'"`) {
				t.Errorf("%s [%s] has a straight quote: %q", key, code, s)
			}
		}
	}
}

// What framy makes is an image in English, as the kind, the library and the
// bot call it, and what a person sends is a photo. "Picture" was a third word
// for the same thing, two rows apart in the settings.
func TestEnglishSaysImageNotPicture(t *testing.T) {
	for _, key := range Keys() {
		if s := translations[key]["en"]; strings.Contains(strings.ToLower(s), "picture") {
			t.Errorf("%s says picture: %q", key, s)
		}
	}
}

// A key with nothing left to pay with is out of credits, as OpenRouter sells
// them and OpenAI and Google bill them, in the bot's card, its key list and
// every line of the site. The site once said "out of money" for the same
// state the bot called "out of credits".
func TestEnglishSaysCreditsNotMoney(t *testing.T) {
	for _, key := range Keys() {
		if s := translations[key]["en"]; strings.Contains(strings.ToLower(s), "money") {
			t.Errorf("%s says money: %q", key, s)
		}
	}
}

// A line of two sentences is a message, so it ends as a sentence does: "No
// results. Open framy" read as one sentence with a stop and one without.
// A label of one sentence (a button, a tag, a row's caption) has no stop
// (docs/design.md, Site, Punctuation).
func TestTwoSentencesEndWithAStop(t *testing.T) {
	inner := regexp.MustCompile(`[.?!] [A-Z{<“]`)
	for _, key := range Keys() {
		s := strings.TrimSpace(translations[key]["en"])
		if inner.MatchString(s) && !strings.ContainsAny(s[len(s)-1:], ".?!…:”)>") {
			t.Errorf("%s has two sentences and no stop at the end: %q", key, s)
		}
	}
}

// A line carries its own full stop: Chinese and Japanese end a sentence with
// 。, so a "." the site's code adds to a translation is wrong in two of the
// sixteen languages.
func TestTheSiteAddsNoFullStop(t *testing.T) {
	files, err := filepath.Glob("../../internal/web/app/src/*.js")
	if err != nil || len(files) == 0 {
		t.Fatalf("no site sources: %v", err)
	}
	stop := regexp.MustCompile(`\+\s*"\.\s?"|"\.\s?"\s*\+`)
	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range stop.FindAllIndex(src, -1) {
			t.Errorf("%s:%d adds a full stop to a line", path, 1+strings.Count(string(src[:m[0]]), "\n"))
		}
	}
}

// One failure is said one way. framy's own failure reads the same in the
// chat and on a failed run of the site (and as the site's refusal with its
// code), and a version that has a newer one reads the same as a tooltip and
// as the line above the variants, which only adds the stop of a message.
// They were "Something went wrong. Try again in a minute.", "It did not
// work. Try again." and "Outdated: 2.6 is newer" beside "There is a newer
// 2.6."
func TestOneFailureIsSaidOneWay(t *testing.T) {
	for _, code := range Codes() {
		if a, b := T(code, "run.failed"), T(code, "web.fail.unknown"); a != b {
			t.Errorf("[%s] the chat says %q, the site %q", code, a, b)
		}
		if a, b := T(code, "web.pick.newer"), T(code, "web.var.outdated"); !strings.HasPrefix(b, a) || len([]rune(b))-len([]rune(a)) != 1 {
			t.Errorf("[%s] a newer version is %q and %q", code, a, b)
		}
	}
}

// "The family" is what the people who make the bots call them; a person who
// knows only framy does not know which family or which "other bots". The
// language is shared with our other bots, as maply says it.
func TestEnglishSaysOurBotsNotTheFamily(t *testing.T) {
	for _, key := range Keys() {
		s := strings.ToLower(translations[key]["en"])
		if strings.Contains(s, "famil") || strings.Contains(s, "other bots") && !strings.Contains(s, "our other bots") {
			t.Errorf("%s speaks of bots the person may not know: %q", key, translations[key]["en"])
		}
	}
}

// panelMark is a symbol a string in Go code may carry although isEmoji
// counts it: the marks of a chosen option (core/docs/panels.md, R7) and the
// flags of the language picker's labels.
func panelMark(r rune) bool {
	return r >= 0x1F1E6 && r <= 0x1F1FF || strings.ContainsRune("✓◉◎■□", r)
}

func TestPanelMarksAreTheOnlyEmojiLeft(t *testing.T) {
	for r, want := range map[rune]bool{'🔑': false, '✅': false, '⭐': false, '⏳': false, '⚠': false, '✨': false, '✓': true, '◉': true, '■': true, '🇬': true} {
		if got := !isEmoji(r) || panelMark(r); got != want {
			t.Errorf("%q allowed in Go strings = %v", r, got)
		}
	}
}

func TestNoEmDashOrEmojiInGoStrings(t *testing.T) {
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
				if tok != token.STRING && tok != token.CHAR {
					continue
				}
				if strings.Contains(lit, emDash) {
					t.Errorf("%s: a string has an em dash: %s", fset.Position(pos), lit)
				}
				// R9: no emoji on a button or in a panel. The flags of the
				// language picker (regional indicators) are its content.
				for _, r := range lit {
					if isEmoji(r) && !panelMark(r) {
						t.Errorf("%s: a string has an emoji %q: %s", fset.Position(pos), r, lit)
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// A key the Go code names must be in the file: the site's lines live there
// too, and a line the site stops using must not leave a panel of the bot
// showing "[web.go.need_key]". A string that looks like a key (a prefix the
// file uses, a dot, and lowercase words) is taken for one, unless it is the
// name of a file.
func TestEveryKeyTheCodeNamesExists(t *testing.T) {
	prefixes := map[string]bool{}
	for _, key := range Keys() {
		prefixes[strings.SplitN(key, ".", 2)[0]] = true
	}
	files := map[string]bool{".mp4": true, ".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".gif": true, ".json": true}
	looksLikeKey := func(s string) bool {
		head, rest, ok := strings.Cut(s, ".")
		if !ok || !prefixes[head] || rest == "" || strings.HasSuffix(rest, ".") || files[filepath.Ext(s)] {
			return false
		}
		for _, r := range rest {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '.') {
				return false
			}
		}
		return true
	}
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
				if tok != token.STRING {
					continue
				}
				v, err := strconv.Unquote(lit)
				if err != nil || !looksLikeKey(v) {
					continue
				}
				if _, ok := translations[v]; !ok {
					t.Errorf("%s: %q is no key of translations.json", fset.Position(pos), v)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// The site's t() shows the key itself for a line that is not there, so a
// typo in a key of internal/web/app reaches the person as "fail.somthing".
// A literal that looks like a key of the site (a head the web.* lines use, a
// dot, lowercase words) or that t() is handed must be one; a literal that
// starts a key, as "set.as_" + of or `fail.${cls}`, must start at least one.
func TestEveryKeyTheSiteNamesExists(t *testing.T) {
	heads := map[string]bool{}
	for _, key := range Keys() {
		if rest, ok := strings.CutPrefix(key, "web."); ok {
			heads[strings.SplitN(rest, ".", 2)[0]] = true
		}
	}
	starts := func(prefix string) bool {
		for _, key := range Keys() {
			if strings.HasPrefix(key, prefix) {
				return true
			}
		}
		return false
	}
	files, err := filepath.Glob("../../internal/web/app/src/*.js")
	if err != nil || len(files) == 0 {
		t.Fatalf("no site sources: %v", err)
	}
	files = append(files, "../../internal/web/app/boot.js")
	checked := 0
	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range siteKey.FindAllSubmatchIndex(src, -1) {
			key, end := string(src[m[4]:m[5]]), string(src[m[6]:m[7]])
			// What t() is handed is a key whatever its head, so a typo in
			// the head ("fial.x") is caught there too.
			if !heads[strings.SplitN(key, ".", 2)[0]] && !calledWith(src[:m[0]]) {
				continue
			}
			line := 1 + strings.Count(string(src[:m[0]]), "\n")
			checked++
			after := strings.TrimLeft(string(src[m[1]:]), " \t\n")
			if end == "${" || strings.HasSuffix(key, ".") || strings.HasPrefix(after, "+") {
				if !starts("web." + key) {
					t.Errorf("%s:%d: no line of translations.json starts with web.%s", path, line, key)
				}
				continue
			}
			if _, ok := translations["web."+key]; !ok {
				t.Errorf("%s:%d: web.%s is no key of translations.json", path, line, key)
			}
		}
	}
	if checked < 100 {
		t.Fatalf("only %d keys found in the site's sources: the scan no longer reads them", checked)
	}
}

// siteKey is a JavaScript string that may be a key: a quote, a key, and a
// closing quote, or a template literal's "${".
var siteKey = regexp.MustCompile("([\"'`])([a-z][a-z0-9_]*\\.[a-z0-9_.]*)(\"|'|`|\\$\\{)")

// calledWith tells whether the source before a string ends in a call of the
// site's t(), so the string is its key.
func calledWith(before []byte) bool {
	s := strings.TrimRight(string(before), " \t\n")
	rest, ok := strings.CutSuffix(s, "t(")
	if !ok {
		return false
	}
	if rest == "" {
		return true
	}
	c := rest[len(rest)-1]
	return !(c == '_' || c == '$' || c == '.' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z')
}

func TestCalledWithReadsOnlyTheSitesT(t *testing.T) {
	for before, want := range map[string]bool{
		`toast(t(`:      true,
		`= t(  `:        true,
		"t(":            true,
		`fmt(`:          false,
		`list.at(`:      false,
		`ask({ title: `: false,
	} {
		if got := calledWith([]byte(before)); got != want {
			t.Errorf("calledWith(%q) = %v", before, got)
		}
	}
}

// The site writes "Free" for the free allowance on the price bar, the payer
// and the model's card. A value of a setting in the same word reads as a
// price: Creativity's "Free" next to the free tier was taken for one.
func TestNoSettingValueReadsAsFree(t *testing.T) {
	for _, code := range Codes() {
		free := map[string]bool{}
		for _, key := range []string{"web.bar.free", "web.det.free", "web.fact.free", "web.pay.free", "web.pay.free_short", "web.prefs.free"} {
			free[strings.ToLower(strings.TrimSpace(translations[key][code]))] = true
		}
		for _, key := range Keys() {
			if strings.HasPrefix(key, "web.val.") && free[strings.ToLower(strings.TrimSpace(translations[key][code]))] {
				t.Errorf("%s [%s] is %q, the word for free", key, code, translations[key][code])
			}
		}
	}
}

// The site keeps a person's runs under the Library tab. A line of the bot
// that sends them to their "history" on the site sends them to a page the
// site does not have.
func TestTheBotNamesTheLibraryAsTheSiteDoes(t *testing.T) {
	if got := T("en", "web.tab.library"); got != "Library" {
		t.Fatalf("the tab is %q now: say it so in the bot's lines too", got)
	}
	for _, key := range Keys() {
		if !strings.HasPrefix(key, "web.") && strings.Contains(strings.ToLower(T("en", key)), "history") {
			t.Errorf("%s sends the person to a history: %q", key, T("en", key))
		}
	}
}

// framy asks without "please", as every other line does: the answer to a
// key pasted into a DM had the only one, after the same two sentences the
// other answer already said.
func TestEnglishAsksWithoutPlease(t *testing.T) {
	for _, key := range Keys() {
		if strings.Contains(strings.ToLower(translations[key]["en"]), "please") {
			t.Errorf("%s says please: %q", key, translations[key]["en"])
		}
	}
}
