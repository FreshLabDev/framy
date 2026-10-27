// SPDX-License-Identifier: Apache-2.0

// Package anim draws the pictures framy puts in a chat before and instead of
// a result (docs/design.md, "/f in a group"): the album preview, where the
// photos fly into a frame and get numbers; the loop shown while a job runs;
// and the card of an error. Every frame is painted on the CPU and the loops
// are encoded to H.264 by ffmpeg, so no browser and no GPU are needed.
//
// The drawing code follows maply's internal/anim, which draws the same kind
// of loops for the maply bot and is the owner's too.
package anim

import (
	"math"
)

// FPS is the frame rate of every loop.
const FPS = 30

// Side is the width and the height of every picture, loop or card. A square
// keeps the message the same size when the preview turns into the loop and
// the loop into the card.
const Side = 720

// The family's look. Field is framy's colour, the proposal in docs/design.md
// (Avatar): deep plum. Every other colour is made from it, as maply's are
// made from its green, so a new field colour is one line.
var (
	Field = Hex("#5A2A5E")
	deep  = Field.Scale(0.72)
	tint  = Field.Mix(white, 0.24)
	ink   = Field.Mix(white, 0.66)
	paper = Field.Mix(white, 0.845)
	white = Hex("#FFFFFF")
)

// Scene is one picture, still or looping. Draw paints the frame at t seconds.
// A loop's frame at Duration looks the same as its frame at 0, so the loop
// has no seam. A still has no duration.
type Scene interface {
	Size() (w, h int)
	Duration() float64
	Draw(c *Canvas, t float64)
}

// Frames is how many frames one loop of the scene has.
func Frames(s Scene) int { return int(math.Round(s.Duration() * FPS)) }

func clamp01(x float64) float64 { return min(1, max(0, x)) }

// lin goes from 0 to 1 as t goes from a to b.
func lin(a, b, t float64) float64 { return clamp01((t - a) / (b - a)) }

func smooth(a, b, x float64) float64 {
	t := lin(a, b, x)
	return t * t * (3 - 2*t)
}

// sine eases in and out: slow at both ends, never faster than 1.6 times the
// even pace.
func sine(t float64) float64 { return -(math.Cos(math.Pi*t) - 1) / 2 }

// back overshoots a little before it settles at 1.
func back(t float64) float64 {
	const c1 = 1.5
	return 1 + (c1+1)*math.Pow(t-1, 3) + c1*math.Pow(t-1, 2)
}
