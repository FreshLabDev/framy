// SPDX-License-Identifier: Apache-2.0

package anim

import "math"

// generatingDur is the length of the loop. Everything in it that moves does
// a whole number of turns or breaths, so its end leads into its start.
const generatingDur = 6.0

// Generating is the loop a preview turns into while a job runs: the
// viewfinder breathes as if it were focusing, the spark in it turns a quarter
// of a turn, and a glint runs round a ring of dots. It has no text and no
// language; the message's caption says what is running.
type Generating struct{}

func (Generating) Size() (int, int)  { return Side, Side }
func (Generating) Duration() float64 { return generatingDur }

func (Generating) Draw(c *Canvas, t float64) {
	const cx, cy, ringR, dots = 360, 402, 262, 72
	c.Fill(Field)
	c.header()
	phase := 2 * math.Pi * t / generatingDur // one turn a loop

	// The ring of dots, with two glints on opposite sides.
	for i := range dots {
		a := float64(i)/dots*2*math.Pi - math.Pi/2
		g := math.Pow(max(0, math.Cos(a-phase)), 30) + math.Pow(max(0, math.Cos(a-phase-math.Pi)), 30)
		g = min(g, 1)
		c.Disc(cx+math.Cos(a)*ringR, cy+math.Sin(a)*ringR, 2.8+3.2*g, tint.Mix(white, g), 1)
	}

	// The viewfinder takes one breath: it closes in and opens again.
	breath := 0.5 - 0.5*math.Cos(phase)
	h := 172 - 16*breath
	c.brackets(cx-h, cy-h, cx+h, cy+h, 64, white, 1)

	// The spark, a quarter turn and two pulses a loop, and four dots that go
	// round with it.
	r := 70 + 7*math.Sin(2*phase)
	rot := phase / 4
	c.spark(cx, cy, r, rot, white, 1)
	for k := range 4 {
		a := rot + math.Pi/4 + float64(k)*math.Pi/2
		c.Disc(cx+math.Cos(a)*118, cy+math.Sin(a)*118, 5, white, 0.9)
	}
}
