// SPDX-License-Identifier: Apache-2.0

package anim

import (
	"strings"

	"github.com/go-text/typesetting/language"
)

// The card is the viewfinder with its cross on the left and the line on the
// right, set from cardTextX in this width, at most this many lines, and in a
// size that goes down from the first to the second until it fits. A card
// without a line has the viewfinder in the middle.
const (
	cardTextX     = 470
	cardTextWidth = 730
	cardMaxLines  = 4
	cardMaxSize   = 52
	cardMinSize   = 34
	cardMarkX     = 250 // the middle of the viewfinder beside a line
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

func (k *Card) Size() (int, int)  { return Width, Height }
func (k *Card) Duration() float64 { return 0 }

func (k *Card) Draw(c *Canvas, _ float64) {
	c.Lang = k.lang
	c.Fill(Field)
	c.header()
	cx, cy, h := Width/2.0, (frameY0+frameY1)/2.0, 150.0
	if k.drawn {
		cx = cardMarkX
	}
	c.brackets(cx-h, cy-h, cx+h, cy+h, 56, white, 1)
	c.cross(cx, cy, 58, stripe, white, 1)
	if !k.drawn {
		return
	}
	// The block of lines is centred on the viewfinder's middle: from the cap
	// height of the first line to the baseline of the last.
	lead, caps := k.size*1.22, k.size*0.7
	y := cy - (lead*float64(len(k.lines)-1)+caps)/2 + caps
	for _, l := range k.lines {
		c.Text(Body, k.size, l, cardTextX, y, Left, white, 1)
		y += lead
	}
}
