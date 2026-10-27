// SPDX-License-Identifier: Apache-2.0

package anim

import (
	"strings"

	"github.com/go-text/typesetting/language"
)

// The text of a card sits in this width, at most this many lines, and in a
// size that goes down from the first to the second until it fits.
const (
	cardTextWidth = 592
	cardMaxLines  = 3
	cardMaxSize   = 46
	cardMinSize   = 30
)

// Card is the picture that replaces a preview when a job fails: the
// viewfinder with a cross in it and one line saying why, in the person's
// language. It is still.
type Card struct {
	lang  language.Language
	lines []string
	size  float64
	drawn bool
}

// NewCard lays out the card. The line is the bot's, already in the language
// lang (a code from internal/i18n); its spaces and line breaks are closed up
// to single spaces. A line with letters the faces lack, Chinese, Japanese and
// Arabic among them, is not drawn at all, and TextDrawn says so.
func NewCard(text, lang string) *Card {
	text = strings.Join(strings.Fields(text), " ")
	card := &Card{lang: language.NewLanguage(lang)}
	if text == "" || !Covers(text) {
		return card
	}
	c := &Canvas{Lang: card.lang}
	for size := float64(cardMaxSize); ; size -= 2 {
		card.size = size
		card.lines = c.lines(Body, size, text, cardTextWidth)
		if size <= cardMinSize || c.fits(card.lines, size) {
			break
		}
	}
	// Still too long at the smallest size: the last line that fits ends in
	// an ellipsis.
	if len(card.lines) > cardMaxLines {
		card.lines[cardMaxLines-1] += " " + strings.Join(card.lines[cardMaxLines:], " ")
		card.lines = card.lines[:cardMaxLines]
	}
	for i, l := range card.lines {
		if c.Measure(Body, card.size, l) > cardTextWidth {
			card.lines[i] = c.shorten(card.size, l)
		}
	}
	card.drawn = len(card.lines) > 0
	return card
}

// fits tells whether the lines are few enough and none is too wide.
func (c *Canvas) fits(lines []string, size float64) bool {
	for _, l := range lines {
		if c.Measure(Body, size, l) > cardTextWidth {
			return false
		}
	}
	return len(lines) <= cardMaxLines
}

// shorten cuts s from the end, a word at a time, until it and an ellipsis fit
// the width; a single word that is too wide loses letters instead.
func (c *Canvas) shorten(size float64, s string) string {
	ellipsis := func(s string) string { return strings.TrimRight(s, " ,;:.-") + "…" }
	words := strings.Fields(s)
	for len(words) > 1 {
		words = words[:len(words)-1]
		if e := ellipsis(strings.Join(words, " ")); c.Measure(Body, size, e) <= cardTextWidth {
			return e
		}
	}
	r := []rune(words[0])
	for len(r) > 1 && c.Measure(Body, size, ellipsis(string(r))) > cardTextWidth {
		r = r[:len(r)-1]
	}
	return ellipsis(string(r))
}

// TextDrawn tells whether the card carries its line.
func (k *Card) TextDrawn() bool { return k.drawn }

func (k *Card) Size() (int, int)  { return Side, Side }
func (k *Card) Duration() float64 { return 0 }

func (k *Card) Draw(c *Canvas, _ float64) {
	c.Lang = k.lang
	c.Fill(Field)
	c.header()
	cx, cy, h := 360.0, 402.0, 150.0
	if k.drawn {
		cy = 322
	}
	c.brackets(cx-h, cy-h, cx+h, cy+h, 56, white, 1)
	c.cross(cx, cy, 58, stripe, white, 1)
	if !k.drawn {
		return
	}
	lead := k.size * 1.22
	y := 526 + k.size*0.35
	for _, l := range k.lines {
		c.Text(Body, k.size, l, cx, y, Center, white, 1)
		y += lead
	}
}
