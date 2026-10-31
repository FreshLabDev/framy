// SPDX-License-Identifier: Apache-2.0

// Package catalog embeds the hand-written part of the model catalog: the
// offers of the direct providers in openai.yaml and gemini.yaml, and
// overlay.yaml with what no API says. internal/catalog reads them.
package catalog

import "embed"

//go:embed *.yaml
var FS embed.FS
