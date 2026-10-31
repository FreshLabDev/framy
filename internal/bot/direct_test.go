// SPDX-License-Identifier: Apache-2.0
package bot

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/i18n"
	"github.com/FreshLabDev/framy/internal/keys"
)

const testSite = "https://framy.example"

type fakeAllowance struct {
	usage free.Usage
	err   error
	// block waits for the read's deadline, as a stuck database would.
	block bool
	calls atomic.Int32
}

func (f *fakeAllowance) Usage(ctx context.Context, _ int64) (free.Usage, error) {
	f.calls.Add(1)
	if f.block {
		<-ctx.Done()
		return free.Usage{}, ctx.Err()
	}
	return f.usage, f.err
}

type fakeConnections struct {
	list  []keys.Connection
	err   error
	calls atomic.Int32
}

func (f *fakeConnections) List(context.Context, int64) ([]keys.Connection, error) {
	f.calls.Add(1)
	return f.list, f.err
}

// fourOfFive is a person with one free image used in the last 24 hours and the
// week's video not used.
func fourOfFive() *fakeAllowance {
	return &fakeAllowance{usage: free.Usage{
		Images: free.Meter{Used: 1, Limit: free.ImagesPerWindow, Window: free.ImageWindow},
		Videos: free.Meter{Used: 0, Limit: free.VideosPerWindow, Window: free.VideoWindow},
	}}
}

func connection(provider string, status keys.Status) keys.Connection {
	return keys.Connection{Provider: provider, Status: status, Last4: "wxyz"}
}

func buildBot(log *slog.Logger, opts ...Option) (*Bot, *fakeStore, *fakeTelegram) {
	store, client := newStore(), newTelegram()
	return New(store, client, log, "framytest_bot", selfID, "v0.1.0", opts...), store, client
}

func newBotWith(opts ...Option) (*Bot, *fakeStore, *fakeTelegram) { return buildBot(quiet(), opts...) }

func webAppButtons(m *tg.InlineKeyboardMarkup) []tg.InlineKeyboardButton {
	var out []tg.InlineKeyboardButton
	for _, b := range buttons(m) {
		if b.WebApp != nil {
			out = append(out, b)
		}
	}
	return out
}

func TestDirectHomeSaysWhatIsLeftWhatIsConnectedAndWhatPricesAreIn(t *testing.T) {
	conns := &fakeConnections{list: []keys.Connection{
		connection(catalog.Gemini, keys.Valid),
		connection(catalog.OpenAI, keys.NoCredits),
		connection(catalog.OpenRouter, keys.Verified),
	}}
	b, _, client := newBotWith(WithAllowance(fourOfFive()), WithConnections(conns), WithSiteURL(testSite))
	if err := b.onMessage(context.Background(), message(dmChat, 7, "/start")); err != nil {
		t.Fatal(err)
	}
	got := client.last()
	want := "<b>framy</b>\n" +
		"<i>Images at the provider’s price.</i>\n\n" +
		"<blockquote>Free images · 4 of 5 left in any 24 hours\n" +
		"Free videos · 1 of 1 left in any 7 days\n" +
		"Your keys · OpenRouter, OpenAI (no credits), Gemini\n" +
		"Language · English\n" +
		"Currency · USD\n" +
		"Not open yet: making images comes with the first test release.</blockquote>"
	if got.method != "sendMessage" || got.chatID != 7 || got.text != want {
		t.Fatalf("sent %s to %d:\n%s\nwant\n%s", got.method, got.chatID, got.text, want)
	}
	if strings.Contains(got.text, "wxyz") {
		t.Fatal("a part of a key is on the panel")
	}
	// Open framy is the first row, alone, and the only Primary button.
	first := got.markup.InlineKeyboard[0]
	if len(first) != 1 || first[0].WebApp == nil || first[0].WebApp.URL != testSite ||
		first[0].Text != "Open framy" || first[0].Style != tg.StylePrimary || first[0].CallbackData != "" {
		t.Fatalf("first row = %+v", first)
	}
	if n := len(webAppButtons(got.markup)); n != 1 {
		t.Fatalf("%d web_app buttons", n)
	}
	if !has(got.markup, actLang) || !has(got.markup, actHelp) || !has(got.markup, actAbout) || has(got.markup, actClose) {
		t.Fatalf("the tabs are gone or Close appeared: %+v", got.markup)
	}
}

// What the strings say about the windows is written in each language; the
// numbers come from the allowance, the words must follow the constants.
func TestHomeStringsNameTheWindowsOfTheAllowance(t *testing.T) {
	if free.ImageWindow != 24*time.Hour || free.VideoWindow != 7*24*time.Hour {
		t.Fatal("the allowance's windows changed: update home.free_images_left and home.free_videos_left in every language")
	}
}

func TestDirectHomeWithNothingToReadStillAnswers(t *testing.T) {
	b, _, client := newBot()
	if err := b.onMessage(context.Background(), message(dmChat, 7, "/start")); err != nil {
		t.Fatal(err)
	}
	got := client.last()
	for _, gone := range []string{"Free images", "Free videos", "Your keys"} {
		if strings.Contains(got.text, gone) {
			t.Errorf("%q on a panel with nothing behind it: %s", gone, got.text)
		}
	}
	for _, kept := range []string{"Language · English", "Currency · USD"} {
		if !strings.Contains(got.text, kept) {
			t.Errorf("%q missing: %s", kept, got.text)
		}
	}
	if len(webAppButtons(got.markup)) != 0 {
		t.Fatal("a web_app button without a site")
	}
}

// Telegram refuses a web_app button that is not https, and a refused panel is
// no panel at all.
func TestOnlyAnHTTPSSiteMakesAButton(t *testing.T) {
	for site, want := range map[string]bool{
		testSite:                true,
		"http://localhost:8080": false,
		"framy.example":         false,
		"":                      false,
	} {
		var logs bytes.Buffer
		b, _, client := buildBot(slog.New(slog.NewTextHandler(&logs, nil)), WithSiteURL(site))
		if err := b.onMessage(context.Background(), message(dmChat, 7, "/start")); err != nil {
			t.Fatal(err)
		}
		if got := len(webAppButtons(client.last().markup)) == 1; got != want {
			t.Errorf("SITE_URL %q: button = %v, want %v", site, got, want)
		}
		if warned := strings.Contains(logs.String(), "not https"); warned != (site != "" && !want) {
			t.Errorf("SITE_URL %q: warned = %v", site, warned)
		}
	}
}

func TestAReadThatFailsLeavesItsLineOutAndThePanelStillAnswers(t *testing.T) {
	var logs bytes.Buffer
	allowance := &fakeAllowance{err: errors.New("allowance down")}
	conns := &fakeConnections{err: errors.New("connections down")}
	b, store, client := buildBot(slog.New(slog.NewTextHandler(&logs, nil)),
		WithAllowance(allowance), WithConnections(conns), WithSiteURL(testSite))
	store.currencyErr = errors.New("currency down")
	if err := b.onMessage(context.Background(), message(dmChat, 7, "/start")); err != nil {
		t.Fatal(err)
	}
	got := client.last()
	if got.method != "sendMessage" {
		t.Fatalf("sent %+v", got)
	}
	for _, gone := range []string{"Free images", "Free videos", "Your keys", "Currency"} {
		if strings.Contains(got.text, gone) {
			t.Errorf("%q stayed after its read failed: %s", gone, got.text)
		}
	}
	if !strings.Contains(got.text, "Language · English") || len(webAppButtons(got.markup)) != 1 {
		t.Fatalf("the rest of the panel is gone: %+v", got)
	}
	for _, what := range []string{"allowance down", "connections down", "currency down"} {
		if !strings.Contains(logs.String(), what) {
			t.Errorf("%q was not logged", what)
		}
	}

	// One failing read does not take the others with it.
	allowance.err = nil
	allowance.usage = fourOfFive().usage
	if err := b.onMessage(context.Background(), message(dmChat, 7, "/start")); err != nil {
		t.Fatal(err)
	}
	if text := client.last().text; !strings.Contains(text, "Free images · 4 of 5 left") || strings.Contains(text, "Your keys") {
		t.Fatalf("after the allowance came back: %s", text)
	}
}

// A database that does not answer must not hold /start back.
func TestASlowReadIsGivenUpOn(t *testing.T) {
	allowance := &fakeAllowance{block: true}
	b, _, client := newBotWith(WithAllowance(allowance), WithSiteURL(testSite))
	b.readTimeout = 50 * time.Millisecond
	started := time.Now()
	if err := b.onMessage(context.Background(), message(dmChat, 7, "/start")); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(started); took > 2*time.Second {
		t.Fatalf("/start took %s", took)
	}
	got := client.last()
	if got.method != "sendMessage" || strings.Contains(got.text, "Free images") || len(webAppButtons(got.markup)) != 1 {
		t.Fatalf("sent %+v", got)
	}
}

// A framy without a KEK file keeps no keys: nothing is connected, which is an
// answer and not a failure.
func TestNoKeyServiceMeansNothingConnected(t *testing.T) {
	for name, opt := range map[string]Option{
		"nil service":   WithConnections((*keys.Service)(nil)),
		"disabled":      WithConnections(&fakeConnections{err: keys.ErrDisabled}),
		"no connection": WithConnections(&fakeConnections{}),
	} {
		var logs bytes.Buffer
		b, _, client := buildBot(slog.New(slog.NewTextHandler(&logs, nil)), opt)
		if err := b.onMessage(context.Background(), message(dmChat, 7, "/start")); err != nil {
			t.Fatal(err)
		}
		if text := client.last().text; !strings.Contains(text, "Your keys · none yet") {
			t.Errorf("%s: %s", name, text)
		}
		if logs.Len() != 0 {
			t.Errorf("%s: logged %q", name, logs.String())
		}
	}
}

func TestOnlyAConnectionThatIsNotValidHasAStatusWord(t *testing.T) {
	v := view{lang: "en", owner: 1, sc: scopePrivate}
	for status, want := range map[keys.Status]string{
		keys.Valid:             "OpenAI",
		keys.Verified:          "OpenAI",
		keys.Invalid:           "OpenAI (invalid key)",
		keys.NeedsBilling:      "OpenAI (needs billing)",
		keys.NeedsVerification: "OpenAI (needs verification)",
		keys.NoCredits:         "OpenAI (no credits)",
		"something new":        "OpenAI",
	} {
		if got := v.connectionList([]keys.Connection{connection(catalog.OpenAI, status)}); got != want {
			t.Errorf("%s: %q, want %q", status, got, want)
		}
	}
	// Providers go in the order the About card names them, and a provider id
	// that is not one of ours cannot break the markup.
	got := v.connectionList([]keys.Connection{
		connection(catalog.Gemini, keys.Valid), connection("<b>", keys.Valid), connection(catalog.OpenRouter, keys.Valid),
	})
	if got != "OpenRouter, Gemini, &lt;b&gt;" {
		t.Errorf("list = %q", got)
	}
}

func TestTheCurrencyIsTheChosenOneThenTheLanguages(t *testing.T) {
	for _, tc := range []struct{ lang, chosen, want string }{
		{"en", "", "USD"},
		{"ru", "", "USD"},
		{"uk", "", "UAH"},
		{"be", "", "BYN"},
		{"pl", "", "PLN"},
		{"uk", "EUR", "EUR"},
		{"en", "UAH", "UAH"},
		{"en", "XYZ", "USD"},
	} {
		b, store, client := newBot()
		store.userLang[7] = tc.lang
		store.currency[7] = tc.chosen
		if err := b.onMessage(context.Background(), message(dmChat, 7, "/start")); err != nil {
			t.Fatal(err)
		}
		if text := client.last().text; !strings.Contains(text, " · "+tc.want+"\n") {
			t.Errorf("%s with %q: %s", tc.lang, tc.chosen, text)
		}
	}
}

func TestTheLanguageLineHasNoFlag(t *testing.T) {
	b, store, client := newBot()
	store.userLang[7] = "uk"
	if err := b.onMessage(context.Background(), message(dmChat, 7, "/start")); err != nil {
		t.Fatal(err)
	}
	text := client.last().text
	if !strings.Contains(text, i18n.T("uk", "lang.title")+" · Українська\n") {
		t.Fatalf("no language line: %s", text)
	}
	for _, r := range text {
		if r >= 0x1F000 {
			t.Fatalf("emoji %q in the panel", r)
		}
	}
}

// Nothing about a person is shown in a group, and Telegram refuses a web_app
// button there.
func TestAGroupPanelHasNoAllowanceNoConnectionsAndNoWebAppButton(t *testing.T) {
	allowance, conns := fourOfFive(), &fakeConnections{list: []keys.Connection{connection(catalog.OpenAI, keys.Valid)}}
	b, _, client := newBotWith(WithAllowance(allowance), WithConnections(conns), WithSiteURL(testSite))
	plain, _, plainClient := newBotWith()
	ctx := context.Background()

	start := message(groupChat, 7, "/start")
	start.EphemeralMessageID = 99
	if err := b.onMessage(ctx, start); err != nil {
		t.Fatal(err)
	}
	if err := plain.onMessage(ctx, start); err != nil {
		t.Fatal(err)
	}
	got := client.last()
	if got.method != "sendEphemeral" || got.text != plainClient.last().text {
		t.Fatalf("the group's panel changed:\n%s\nwas\n%s", got.text, plainClient.last().text)
	}
	if len(webAppButtons(got.markup)) != 0 {
		t.Fatal("a web_app button in a group")
	}

	// Back to home in a group redraws the same.
	if err := b.onCallback(ctx, callback(groupChat, 7, 7, actHome, 99)); err != nil {
		t.Fatal(err)
	}
	if got := client.last(); got.method != "editEphemeral" || len(webAppButtons(got.markup)) != 0 || strings.Contains(got.text, "Free") {
		t.Fatalf("home in a group = %+v", got)
	}
	if allowance.calls.Load() != 0 || conns.calls.Load() != 0 {
		t.Fatal("a person's allowance or connections were read for a group")
	}
}

// Back from a tab redraws the home panel with the person's facts, read anew.
func TestBackToTheDirectHomeReadsAgain(t *testing.T) {
	allowance := fourOfFive()
	b, _, client := newBotWith(WithAllowance(allowance), WithSiteURL(testSite))
	ctx := context.Background()
	if err := b.onCallback(ctx, callback(dmChat, 7, 7, actHelp, 0)); err != nil {
		t.Fatal(err)
	}
	if allowance.calls.Load() != 0 {
		t.Fatal("the help tab read the allowance")
	}
	allowance.usage.Images.Used = 3
	if err := b.onCallback(ctx, callback(dmChat, 7, 7, actHome, 0)); err != nil {
		t.Fatal(err)
	}
	got := client.last()
	if got.method != "editMessageText" || !strings.Contains(got.text, "Free images · 2 of 5 left in any 24 hours") ||
		len(webAppButtons(got.markup)) != 1 {
		t.Fatalf("home = %+v", got)
	}
}

// The strings of the new lines exist and read as words in every language.
func TestDirectHomeInEveryLanguage(t *testing.T) {
	sample := account{
		appURL:         testSite,
		usage:          fourOfFive().usage,
		hasUsage:       true,
		hasConnections: true,
		connections: []keys.Connection{
			connection(catalog.OpenRouter, keys.Invalid), connection(catalog.OpenAI, keys.NeedsVerification),
			connection(catalog.Gemini, keys.NeedsBilling), connection("other", keys.NoCredits),
		},
		currency: "UAH",
	}
	for _, lang := range i18n.Codes() {
		v := view{lang: lang, owner: longOwner, sc: scopePrivate, account: sample}
		text, m := v.home(false, "")
		for _, leak := range []string{"[", "{", "}"} {
			if strings.Contains(text, leak) {
				t.Errorf("%s: %q in %s", lang, leak, text)
			}
		}
		for _, line := range []string{
			i18n.T(lang, "home.free_images_left", "left", "4", "limit", "5"),
			i18n.T(lang, "home.free_videos_left", "left", "1", "limit", "1"),
			i18n.T(lang, "home.status_invalid"), i18n.T(lang, "home.status_verification"),
			i18n.T(lang, "home.status_billing"), i18n.T(lang, "home.status_credits"),
			i18n.NameOf(lang),
		} {
			if !strings.Contains(text, line) {
				t.Errorf("%s: %q missing from %s", lang, line, text)
			}
		}
		if got := m.InlineKeyboard[0][0]; got.Text != i18n.T(lang, "btn.open") || got.WebApp == nil {
			t.Errorf("%s: first button %+v", lang, got)
		}
	}
}

// runStart runs the bot through its start and one empty poll, and returns what
// Telegram was asked.
func runStart(t *testing.T, log *slog.Logger, client *fakeTelegram, opts ...Option) *Bot {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client.cancel = cancel
	b := New(newStore(), client, log, "framytest_bot", selfID, "v0.1.0", opts...)
	if err := b.Run(ctx); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestTheMenuButtonOpensTheSiteWhenThereIsOne(t *testing.T) {
	t.Run("with a site", func(t *testing.T) {
		client := newTelegram()
		runStart(t, quiet(), client, WithSiteURL(testSite))
		if len(client.menuButtons) != 1 {
			t.Fatalf("menu buttons = %+v", client.menuButtons)
		}
		got := client.menuButtons[0]
		if got.chatID != 0 || got.button.Type != tg.MenuWebApp || got.button.Text != "framy" ||
			got.button.WebApp == nil || got.button.WebApp.URL != testSite {
			t.Fatalf("menu button = %+v", got)
		}
	})
	for name, site := range map[string]string{"without a site": "", "with an address Telegram would refuse": "http://localhost:8080"} {
		t.Run(name, func(t *testing.T) {
			client := newTelegram()
			b := runStart(t, quiet(), client, WithSiteURL(site))
			if len(client.menuButtons) != 0 {
				t.Fatalf("the menu button was touched: %+v", client.menuButtons)
			}
			if !b.Initialized() || client.writes == 0 {
				t.Fatal("the rest of the start did not happen")
			}
		})
	}
	t.Run("a refusal is logged and polling goes on", func(t *testing.T) {
		var logs bytes.Buffer
		client := newTelegram()
		client.menuErr = errors.New("menu refused")
		runStart(t, slog.New(slog.NewTextHandler(&logs, nil)), client, WithSiteURL(testSite))
		if !strings.Contains(logs.String(), "menu refused") || len(client.offsets) != 2 {
			t.Fatalf("logs %q, polled %v", logs.String(), client.offsets)
		}
	})
}
