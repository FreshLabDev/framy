// SPDX-License-Identifier: Apache-2.0
package bot

import (
	"context"
	"strings"
	"testing"
	"unicode"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/i18n"
)

// A Telegram user id is up to 52 bits; the longest owner keeps every callback
// honest about Telegram's 64-byte limit.
const longOwner = int64(4503599627370495)

type drawn struct {
	name   string
	text   string
	markup *tg.InlineKeyboardMarkup
}

func everyPanel(v view) []drawn {
	var out []drawn
	add := func(name, text string, m *tg.InlineKeyboardMarkup) { out = append(out, drawn{name, text, m}) }
	text, m := v.home(true, "uk")
	add("home", text, m)
	text, m = v.home(false, "")
	add("member home", text, m)
	text, m = v.help()
	add("help", text, m)
	text, m = v.about("v0.1.0")
	add("about", text, m)
	text, m = v.language()
	add("language", text, m)
	text, m = v.groupLanguage("uk")
	add("group language", text, m)
	return out
}

func buttons(m *tg.InlineKeyboardMarkup) []tg.InlineKeyboardButton {
	var all []tg.InlineKeyboardButton
	for _, r := range m.InlineKeyboard {
		all = append(all, r...)
	}
	return all
}

func has(m *tg.InlineKeyboardMarkup, action string) bool {
	for _, b := range buttons(m) {
		if strings.HasSuffix(b.CallbackData, ":"+action) {
			return true
		}
	}
	return false
}

// R1, and nothing leaks: every screen is a bold title, an italic hint and a
// quoted body, with no "[key]" or "{placeholder}" left in it.
func TestEveryPanelIsOneShape(t *testing.T) {
	for _, lang := range i18n.Codes() {
		for _, sc := range []scope{scopePrivate, scopeGroup} {
			for _, p := range everyPanel(view{lang: lang, owner: longOwner, sc: sc}) {
				lines := strings.SplitN(p.text, "\n", 3)
				if !strings.HasPrefix(lines[0], "<b>") || len(lines) < 2 ||
					!strings.HasPrefix(lines[1], "<i>") || !strings.HasSuffix(lines[1], "</i>") {
					t.Errorf("%s/%s: not title, hint: %q", lang, p.name, p.text)
				}
				if len(lines) == 3 && !strings.HasPrefix(lines[2], "\n<blockquote>") {
					t.Errorf("%s/%s: the body is not quoted: %q", lang, p.name, p.text)
				}
				if strings.Contains(p.text, "<blockquote></blockquote>") {
					t.Errorf("%s/%s: an empty quote", lang, p.name)
				}
				if strings.Contains(p.text, "[") || strings.Contains(p.text, "{") {
					t.Errorf("%s/%s: a key or placeholder leaked: %q", lang, p.name, p.text)
				}
				for _, b := range buttons(p.markup) {
					if len(b.CallbackData) > 64 {
						t.Errorf("%s/%s: callback %q is %d bytes", lang, p.name, b.CallbackData, len(b.CallbackData))
					}
					if strings.Contains(b.Text, "[") || strings.Contains(b.Text, "{") {
						t.Errorf("%s/%s: button %q", lang, p.name, b.Text)
					}
				}
			}
		}
	}
}

// R4, R5, R6: at most one Primary; Success only on the marked option of a
// set; Danger only on Close.
func TestButtonColours(t *testing.T) {
	for _, sc := range []scope{scopePrivate, scopeGroup} {
		for _, p := range everyPanel(view{lang: "en", owner: 1, sc: sc}) {
			primaries := 0
			for _, b := range buttons(p.markup) {
				switch b.Style {
				case tg.StylePrimary:
					primaries++
				case tg.StyleSuccess:
					if !strings.HasPrefix(b.Text, markOn) {
						t.Errorf("%s: Success on %q, which is not the chosen option", p.name, b.Text)
					}
				case tg.StyleDanger:
					if !strings.HasSuffix(b.CallbackData, ":"+actClose) {
						t.Errorf("%s: Danger on %q", p.name, b.Text)
					}
				}
			}
			if primaries > 1 {
				t.Errorf("%s: %d Primary buttons", p.name, primaries)
			}
		}
	}
}

// R9: no emoji in a panel. The flags of the language picker are content.
func TestNoEmojiOutsideTheLanguagePicker(t *testing.T) {
	for _, sc := range []scope{scopePrivate, scopeGroup} {
		for _, p := range everyPanel(view{lang: "en", owner: 1, sc: sc}) {
			for _, r := range p.text {
				if r >= 0x1F000 {
					t.Errorf("%s: emoji %q in the text", p.name, r)
				}
			}
			for _, b := range buttons(p.markup) {
				if strings.Contains(b.CallbackData, ":"+actLang+":") || strings.Contains(b.CallbackData, ":"+actGroupLang+":") {
					continue
				}
				if strings.HasSuffix(b.CallbackData, ":"+actGroupLang) {
					// "Group language: 🇺🇦 Українська" names the current language by its picker label.
					continue
				}
				for _, r := range b.Text {
					if r >= 0x1F000 || unicode.Is(unicode.So, r) && !strings.ContainsRune(markOn+markOff, r) {
						t.Errorf("%s: emoji %q on %q", p.name, r, b.Text)
					}
				}
			}
		}
	}
}

// R3: Close exists only where the panel is a message in somebody else's feed.
func TestCloseOnlyInGroups(t *testing.T) {
	for _, sc := range []scope{scopePrivate, scopeGroup} {
		for _, p := range everyPanel(view{lang: "en", owner: 1, sc: sc}) {
			if got := has(p.markup, actClose); got != (sc == scopeGroup) {
				t.Errorf("scope %d, %s: close = %v", sc, p.name, got)
			}
		}
	}
}

func TestGroupLanguageIsOnAnAdminsHomeOnly(t *testing.T) {
	group := view{lang: "en", owner: 1, sc: scopeGroup}
	_, member := group.home(false, "")
	_, admin := group.home(true, "")
	if has(member, actGroupLang) || !has(admin, actGroupLang) {
		t.Fatal("the group's language must be on an admin's home and only there")
	}
	dm := view{lang: "en", owner: 1, sc: scopePrivate}
	if _, m := dm.home(true, ""); !has(m, actLang) || has(m, actGroupLang) {
		t.Fatal("a direct chat's home offers the person's language, not a group's")
	}
}

// R7, R8: one option of the sixteen is green and marked, every other is
// marked as not chosen, in the family's order, two per row.
func TestLanguageGridMarksTheCurrentOne(t *testing.T) {
	v := view{lang: "uk", owner: 1, sc: scopePrivate}
	_, m := v.language()
	var options []tg.InlineKeyboardButton
	for _, b := range buttons(m) {
		if strings.Contains(b.CallbackData, ":"+actLang+":") {
			options = append(options, b)
		}
	}
	if len(options) != len(i18n.LANGUAGE_OPTIONS) {
		t.Fatalf("%d options, want %d", len(options), len(i18n.LANGUAGE_OPTIONS))
	}
	for i, b := range options {
		code := i18n.LANGUAGE_OPTIONS[i].Code
		chosen := code == "uk"
		if !strings.HasSuffix(b.CallbackData, ":"+code) {
			t.Fatalf("option %d is %q, want %s", i, b.CallbackData, code)
		}
		if chosen != strings.HasPrefix(b.Text, markOn) || chosen != (b.Style == tg.StyleSuccess) {
			t.Fatalf("%s: text %q style %q", code, b.Text, b.Style)
		}
		if !chosen && !strings.HasPrefix(b.Text, markOff) {
			t.Fatalf("%s: not marked as not chosen: %q", code, b.Text)
		}
	}
	if len(m.InlineKeyboard[0]) != 2 {
		t.Fatal("languages go two per row")
	}
	if !has(m, actLangAuto) {
		t.Fatal("no Follow Telegram")
	}
}

// The About card is exactly the family's: name and version, one line, then
// one fact, the source and the admin, with no commit and no date.
func TestAboutCardIsExactly(t *testing.T) {
	text, m := view{lang: "en", owner: 1, sc: scopePrivate}.about("v0.1.0-alpha.1")
	want := "<b>framy</b> · <i>v0.1.0-alpha.1</i>\n" +
		"<i>Images from OpenRouter, OpenAI and Google models.</i>\n\n" +
		"<blockquote>Models · OpenRouter · OpenAI · Google\n" +
		"Source · <a href=\"https://github.com/FreshLabDev/framy\">FreshLabDev/framy</a> · Apache-2.0\n" +
		"Admin · <a href=\"https://t.me/amtiyo\">@amtiyo</a></blockquote>"
	if text != want {
		t.Fatalf("about =\n%s\nwant\n%s", text, want)
	}
	for _, b := range buttons(m) {
		if b.URL != "" {
			t.Fatal("the repository is a link in the text, never also a button")
		}
	}
}

func newBot() (*Bot, *fakeStore, *fakeTelegram) {
	store, client := newStore(), newTelegram()
	return New(store, client, quiet(), "framytest_bot", selfID, "v0.1.0"), store, client
}

func message(chat tg.Chat, from int64, text string) *tg.Message {
	return &tg.Message{MessageID: 10, Chat: chat, From: &tg.User{ID: from, LanguageCode: "en"}, Text: text}
}

var (
	dmChat    = tg.Chat{ID: 7, Type: "private"}
	groupChat = tg.Chat{ID: -100, Type: "supergroup"}
)

func TestStartIsOursOnly(t *testing.T) {
	b, _, _ := newBot()
	for text, want := range map[string]bool{
		"/start":                       true,
		"/start@framytest_bot":         true,
		"/start@FRAMYTEST_BOT payload": true,
		"/start@other_bot":             false,
		"start":                        false,
		"hello /start":                 false,
	} {
		cmd, ok := b.command(message(dmChat, 7, text))
		if (ok && cmd == "start") != want {
			t.Errorf("%q: start = %v", text, ok && cmd == "start")
		}
	}
}

func TestStartInADMIsAPanelInThePersonsLanguage(t *testing.T) {
	b, store, client := newBot()
	store.userLang[7] = "uk"
	if err := b.onMessage(context.Background(), message(dmChat, 7, "/start")); err != nil {
		t.Fatal(err)
	}
	got := client.last()
	if got.method != "sendMessage" || got.chatID != 7 || !strings.Contains(got.text, i18n.T("uk", "home.body")) {
		t.Fatalf("sent %+v", got)
	}
}

// In a group an ordinary /start would put one person's menu in everyone's
// feed. The ephemeral one is answered ephemerally, to its sender only.
func TestStartInAGroupIsEphemeral(t *testing.T) {
	b, _, client := newBot()
	ctx := context.Background()
	if err := b.onMessage(ctx, message(groupChat, 7, "/start")); err != nil {
		t.Fatal(err)
	}
	if len(client.calls) != 0 {
		t.Fatalf("an ordinary group /start was answered: %+v", client.calls)
	}
	m := message(groupChat, 7, "/start")
	m.EphemeralMessageID = 99
	if err := b.onMessage(ctx, m); err != nil {
		t.Fatal(err)
	}
	got := client.last()
	if got.method != "sendEphemeral" || got.receiver != 7 || got.id != 99 || !has(got.markup, actClose) {
		t.Fatalf("sent %+v", got)
	}
	for _, cmd := range []string{"/help", "/about", "/language"} {
		if err := b.onMessage(ctx, message(groupChat, 7, cmd)); err != nil {
			t.Fatal(err)
		}
	}
	if len(client.calls) != 1 {
		t.Fatalf("direct-chat tabs answered in a group: %+v", client.calls)
	}
}

func callback(chat tg.Chat, from, owner int64, action string, ephemeralID int64) *tg.CallbackQuery {
	return &tg.CallbackQuery{
		ID:      "cq",
		From:    tg.User{ID: from, LanguageCode: "en"},
		Message: tg.Message{MessageID: 10, Chat: chat, Text: "panel", EphemeralMessageID: ephemeralID},
		Data:    cb(owner, action),
	}
}

func TestOnlyTheOwnerPresses(t *testing.T) {
	b, _, client := newBot()
	if err := b.onCallback(context.Background(), callback(dmChat, 8, 7, actHelp, 0)); err != nil {
		t.Fatal(err)
	}
	if len(client.calls) != 0 || len(client.alerts) != 1 || client.alerts[0] != i18n.T("en", "msg.not_yours") {
		t.Fatalf("calls %+v, alerts %v", client.calls, client.alerts)
	}
}

// In a group only an ephemeral panel can be driven: a forged callback against
// someone's public message must change nothing.
func TestOnlyAnEphemeralPanelIsAddressableInAGroup(t *testing.T) {
	b, _, client := newBot()
	ctx := context.Background()
	if err := b.onCallback(ctx, callback(groupChat, 7, 7, actClose, 0)); err != nil {
		t.Fatal(err)
	}
	if len(client.calls) != 0 {
		t.Fatalf("a public group message was acted on: %+v", client.calls)
	}
	if err := b.onCallback(ctx, callback(groupChat, 7, 7, actClose, 99)); err != nil {
		t.Fatal(err)
	}
	if got := client.last(); got.method != "deleteEphemeral" || got.id != 99 || got.receiver != 7 {
		t.Fatalf("close = %+v", got)
	}
	if err := b.onCallback(ctx, callback(groupChat, 7, 7, actHelp, 99)); err != nil {
		t.Fatal(err)
	}
	if got := client.last(); got.method != "editEphemeral" || got.id != 99 {
		t.Fatalf("help = %+v", got)
	}
}

func TestPersonalLanguageAndFollowTelegram(t *testing.T) {
	b, store, client := newBot()
	ctx := context.Background()
	store.fallback[7] = "de"
	if err := b.onCallback(ctx, callback(dmChat, 7, 7, actLang+":pl", 0)); err != nil {
		t.Fatal(err)
	}
	if store.userLang[7] != "pl" {
		t.Fatalf("language = %q", store.userLang[7])
	}
	if got := client.last(); got.method != "editMessageText" || !strings.Contains(got.text, i18n.T("pl", "lang.hint")) {
		t.Fatalf("redraw = %+v", got)
	}
	// Follow Telegram redraws in whatever core answers next, not in the
	// client's code.
	if err := b.onCallback(ctx, callback(dmChat, 7, 7, actLangAuto, 0)); err != nil {
		t.Fatal(err)
	}
	if got := client.last(); !strings.Contains(got.text, i18n.T("de", "lang.hint")) {
		t.Fatalf("after follow: %q", got.text)
	}
	// A personal pick is not offered in a group, and a forged one does nothing.
	if err := b.onCallback(ctx, callback(groupChat, 7, 7, actLang+":uk", 99)); err != nil {
		t.Fatal(err)
	}
	if store.userLang[7] != "de" {
		t.Fatalf("a group panel changed the person's language to %q", store.userLang[7])
	}
}

func TestGroupLanguageIsAdminOnly(t *testing.T) {
	b, store, client := newBot()
	ctx := context.Background()
	if err := b.onCallback(ctx, callback(groupChat, 7, 7, actGroupLang+":uk", 99)); err != nil {
		t.Fatal(err)
	}
	if store.chatLang[-100] != "" || len(client.alerts) != 1 || client.alerts[0] != i18n.T("en", "msg.admin_only") {
		t.Fatalf("a member changed the group's language: %v, alerts %v", store.chatLang, client.alerts)
	}
	client.statuses[7] = "administrator"
	if err := b.onCallback(ctx, callback(groupChat, 7, 7, actGroupLang+":uk", 99)); err != nil {
		t.Fatal(err)
	}
	if store.chatLang[-100] != "uk" {
		t.Fatalf("group language = %q", store.chatLang[-100])
	}
	if got := client.last(); got.method != "editEphemeral" || !strings.Contains(got.text, i18n.T("uk", "lang.group_hint")) {
		t.Fatalf("redraw = %+v", got)
	}
	if err := b.onCallback(ctx, callback(groupChat, 7, 7, actGroupAuto, 99)); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.chatLang[-100]; ok {
		t.Fatal("Automatic left the group's language set")
	}
}

func TestProfileIsPublishedPerLanguageAndWrittenOnlyOnChange(t *testing.T) {
	b, _, client := newBot()
	ctx := context.Background()
	b.publishProfile(ctx)
	for _, code := range append([]string{""}, i18n.Codes()...) {
		private := client.commands["all_private_chats/"+code]
		group := client.commands["all_group_chats/"+code]
		if len(private) != 1 || private[0].Command != "start" || private[0].IsEphemeral {
			t.Fatalf("%q private commands = %+v", code, private)
		}
		if len(group) != 1 || group[0].Command != "start" || !group[0].IsEphemeral {
			t.Fatalf("%q group commands = %+v", code, group)
		}
	}
	if client.profile["short_description/uk"] != i18n.T("uk", "bot.short") || client.profile["description/"] != i18n.T("en", "bot.description") {
		t.Fatalf("profile = %v", client.profile)
	}
	first := client.writes
	if first != 2*(len(i18n.Codes())+1) {
		t.Fatalf("%d writes on the first start", first)
	}
	b.publishProfile(ctx)
	if client.writes != first {
		t.Fatalf("a restart wrote %d unchanged texts again", client.writes-first)
	}
}
