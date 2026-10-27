// SPDX-License-Identifier: Apache-2.0
package bot

import (
	"html"
	"strconv"
	"strings"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/i18n"
)

// The menu follows the family's panel contract (core/docs/panels.md):
// callbacks "m:<owner>:<action>", one panel shape, the same marks for a
// choice, Back to go up, Close only in a group.
const menuPrefix = "m"

const (
	markOn  = "◉ "
	markOff = "◎ "
)

const (
	productName = "framy"
	repoURL     = "https://github.com/FreshLabDev/framy"
	repoName    = "FreshLabDev/framy"
	licenseName = "Apache-2.0"
	adminURL    = "https://t.me/amtiyo"
	adminName   = "@amtiyo"
	// providers are brand names, written the same in every language.
	providers = "OpenRouter · OpenAI · Google"
)

// Actions a panel button carries. They are a contract with panels already
// sent: a renamed action leaves old buttons doing nothing.
const (
	actHome      = "home"
	actHelp      = "help"
	actAbout     = "about"
	actLang      = "lang"
	actLangAuto  = "langauto"
	actGroupLang = "glang"
	actGroupAuto = "glangauto"
	actClose     = "close"
)

// adminOnly reports whether an action changes something that belongs to the
// whole group. The check asks Telegram on every press: rights change.
func adminOnly(action string) bool {
	head, _, _ := strings.Cut(action, ":")
	return head == actGroupLang || head == actGroupAuto
}

func cb(owner int64, action string) string {
	return menuPrefix + ":" + strconv.FormatInt(owner, 10) + ":" + action
}

func parseMenuCB(data string) (owner int64, action string, ok bool) {
	parts := strings.SplitN(data, ":", 3)
	if len(parts) != 3 || parts[0] != menuPrefix || parts[2] == "" {
		return 0, "", false
	}
	owner, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || owner == 0 {
		return 0, "", false
	}
	return owner, parts[2], true
}

// scope is where the panel sits. In a direct chat the conversation is the
// panel. In a group it is one person's menu in everyone else's feed, so it can
// be closed, and what it changes belongs to the whole group.
type scope int

const (
	scopePrivate scope = iota
	scopeGroup
)

func scopeOf(chat tg.Chat) scope {
	if chat.Type == "private" {
		return scopePrivate
	}
	return scopeGroup
}

// view is who a panel is drawn for: the language it is written in, the person
// whose buttons these are, and where it sits. account is read for the home
// panel of a direct chat only; empty, that panel says nothing of the person
// beyond their language.
type view struct {
	lang    string
	owner   int64
	sc      scope
	account account
}

type row = []tg.InlineKeyboardButton

func (v view) t(key string, pairs ...string) string { return i18n.T(v.lang, key, pairs...) }

func (v view) button(key, action string) tg.InlineKeyboardButton {
	return tg.InlineKeyboardButton{Text: v.t(key), CallbackData: cb(v.owner, action)}
}

func markup(rows ...row) *tg.InlineKeyboardMarkup {
	return &tg.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// panel is the one shape every screen has (R1): a bold title, an italic hint
// under it, then the substance in a quote. note sits on the title line; only
// About has one, the version.
func panel(title, note, hint, body string) string {
	s := "<b>" + html.EscapeString(title) + "</b>"
	if note != "" {
		s += " · <i>" + html.EscapeString(note) + "</i>"
	}
	if hint != "" {
		s += "\n<i>" + hint + "</i>"
	}
	if body != "" {
		s += "\n\n" + body
	}
	return s
}

// quote wraps the substance. A screen whose substance is its keyboard has
// none, rather than an empty one.
func quote(lines ...string) string {
	inner := strings.TrimSpace(strings.Join(lines, "\n"))
	if inner == "" {
		return ""
	}
	return "<blockquote>" + inner + "</blockquote>"
}

func link(url, label string) string {
	return "<a href=\"" + url + "\">" + html.EscapeString(label) + "</a>"
}

// selectable is one option of a set (R7). The current one is state, not an
// action, so it is the one painted Success (R5).
func selectable(text, data string, selected bool) tg.InlineKeyboardButton {
	b := tg.InlineKeyboardButton{Text: markOff + text, CallbackData: data}
	if selected {
		b.Text = markOn + text
		b.Style = tg.StyleSuccess
	}
	return b
}

func pairs(buttons []tg.InlineKeyboardButton) []row {
	rows := make([]row, 0, (len(buttons)+1)/2)
	for i := 0; i < len(buttons); i += 2 {
		rows = append(rows, buttons[i:min(i+2, len(buttons))])
	}
	return rows
}

// navRow is Back (R2), and Close only in a group (R3).
func (v view) navRow(back string) row {
	r := row{v.button("btn.back", back)}
	if v.sc == scopeGroup {
		r = append(r, v.closeButton())
	}
	return r
}

func (v view) closeButton() tg.InlineKeyboardButton {
	b := v.button("btn.close", actClose)
	b.Style = tg.StyleDanger
	return b
}

// home is two screens. The direct chat gets every tab, since there the panel
// is the whole bot; a group gets what a group acts on, and its language only
// for an admin, who is the only one who can change it. In a direct chat Open
// framy leads, so it is the Primary button (R4); a group's panel has nothing
// that leads yet, and generating will.
func (v view) home(admin bool, groupLang string) (string, *tg.InlineKeyboardMarkup) {
	if v.sc == scopeGroup {
		text := panel(productName, "", v.t("home.hint"), quote(v.t("home.group"), v.t("home.soon")))
		var rows []row
		if admin {
			rows = append(rows, row{{Text: v.t("btn.group_language", "language", v.languageLabel(groupLang)), CallbackData: cb(v.owner, actGroupLang)}})
		}
		rows = append(rows,
			row{v.button("btn.help", actHelp), v.button("btn.about", actAbout)},
			// Home is the top, so it has nothing to go back to; it still closes.
			row{v.closeButton()},
		)
		return text, markup(rows...)
	}
	text := panel(productName, "", v.t("home.hint"), quote(append(v.accountLines(), v.t("home.soon"))...))
	var rows []row
	if v.account.appURL != "" {
		rows = append(rows, row{v.openButton(v.account.appURL)})
	}
	rows = append(rows,
		row{v.button("btn.language", actLang), v.button("btn.help", actHelp)},
		row{v.button("btn.about", actAbout)},
	)
	return text, markup(rows...)
}

func (v view) languageLabel(code string) string {
	if code == "" {
		return v.t("btn.lang_auto")
	}
	return i18n.LabelOf(code)
}

func (v view) help() (string, *tg.InlineKeyboardMarkup) {
	return panel(v.t("help.title"), "", v.t("help.hint"), quote(v.t("help.body"))),
		markup(v.navRow(actHome))
}

// about is exactly the family's About card: name and version, one line of what
// the bot is, then one fact, the source and the admin. The version comes from
// the build, never a commit or a date.
func (v view) about(version string) (string, *tg.InlineKeyboardMarkup) {
	body := quote(
		v.t("about.models")+" · "+providers,
		v.t("about.source")+" · "+link(repoURL, repoName)+" · "+licenseName,
		v.t("about.admin")+" · "+link(adminURL, adminName),
	)
	return panel(productName, version, v.t("about.tagline"), body), markup(v.navRow(actHome))
}

// languageGrid is the family's sixteen languages, two per row (R8), with the
// current one marked.
func (v view) languageGrid(current, action string) []row {
	buttons := make([]tg.InlineKeyboardButton, len(i18n.LANGUAGE_OPTIONS))
	for i, opt := range i18n.LANGUAGE_OPTIONS {
		buttons[i] = selectable(opt.Label, cb(v.owner, action+":"+opt.Code), opt.Code == current)
	}
	return pairs(buttons)
}

// language is the person's interface language, shared with the sibling bots
// through core. Follow Telegram is the way back from a manual pick, which
// otherwise outranks every automatic source for ever. It acts, so it carries
// no mark and no colour.
func (v view) language() (string, *tg.InlineKeyboardMarkup) {
	rows := v.languageGrid(v.lang, actLang)
	rows = append(rows, row{v.button("btn.follow_telegram", actLangAuto)}, v.navRow(actHome))
	return panel(v.t("lang.title"), "", v.t("lang.hint"), ""), markup(rows...)
}

// groupLanguage is the group's own language, in which framy writes there.
// With none set the group follows core's automatic answer.
func (v view) groupLanguage(current string) (string, *tg.InlineKeyboardMarkup) {
	rows := v.languageGrid(current, actGroupLang)
	rows = append(rows, row{v.button("btn.lang_auto", actGroupAuto)}, v.navRow(actHome))
	return panel(v.t("lang.title"), "", v.t("lang.group_hint"), ""), markup(rows...)
}
