// SPDX-License-Identifier: Apache-2.0

// Package answer reads the JSON answers of the image APIs, which carry each
// picture as a base64 string, without holding the whole answer, its strings
// and the decoded pictures in memory at once. Read whole, an answer took
// about 3.3 times its size: the decoder's buffer, a string per picture and
// the bytes. A run may answer with 256 MiB, and the container has 1 GiB for
// every worker (docs/design.md, Workers).
package answer

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Base64 is a file an answer carries as base64, bare or as a data URL,
// decoded straight from the JSON text into bytes, with no string between.
// Text that is not base64 does not stop the answer around it from being
// read: Err says what was wrong, so the run keeps its cost and its other
// files.
type Base64 struct {
	Data []byte
	// MediaType is the type a data URL names; "" for bare base64.
	MediaType string
	// Err: the text was not base64, or a data URL had no data.
	Err error
}

// UnmarshalJSON decodes the string. null leaves b empty.
func (b *Base64) UnmarshalJSON(raw []byte) error {
	*b = Base64{}
	if string(raw) == "null" {
		return nil
	}
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return errors.New("base64 data that is not a string")
	}
	text := raw[1 : len(raw)-1]
	if bytes.IndexByte(text, '\\') >= 0 {
		// Escapes, which no provider writes in base64 ("\/" is legal JSON):
		// the slow way, through a string.
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return err
		}
		text = []byte(s)
	}
	if rest, ok := bytes.CutPrefix(text, []byte("data:")); ok {
		meta, data, found := bytes.Cut(rest, []byte(","))
		if !found {
			b.Err = errors.New("a data URL with no data")
			return nil
		}
		mediaType, _, _ := bytes.Cut(meta, []byte(";"))
		b.MediaType, text = string(mediaType), data
	}
	data := make([]byte, base64.StdEncoding.DecodedLen(len(text)))
	n, err := base64.StdEncoding.Decode(data, text)
	if err != nil {
		b.Err = fmt.Errorf("decode base64: %w", err)
		return nil
	}
	b.Data = data[:n:n]
	return nil
}

// Each decodes the JSON object r holds as it reads it: every element of the
// array under the member list goes to each, one at a time, and the other
// members go to rest at the end, as json.Unmarshal fills them. Only one
// element is in memory at once beside what each keeps; a list that is null
// or absent has no elements. The error of a reader, such as
// *http.MaxBytesError, comes back as it is.
func Each(r io.Reader, list string, each func(dec *json.Decoder) error, rest any) error {
	dec := json.NewDecoder(r)
	if err := delim(dec, '{'); err != nil {
		return err
	}
	others := []byte{'{'}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		name, _ := tok.(string)
		if name == list {
			if err := elements(dec, each); err != nil {
				return err
			}
			continue
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return err
		}
		if len(others) > 1 {
			others = append(others, ',')
		}
		quoted, err := json.Marshal(name)
		if err != nil {
			return err
		}
		others = append(append(append(others, quoted...), ':'), v...)
	}
	if err := delim(dec, '}'); err != nil {
		return err
	}
	return json.Unmarshal(append(others, '}'), rest)
}

func elements(dec *json.Decoder, each func(dec *json.Decoder) error) error {
	tok, err := dec.Token()
	switch {
	case err != nil:
		return err
	case tok == nil:
		return nil
	case tok != json.Delim('['):
		return fmt.Errorf("the list is %v, not an array", tok)
	}
	for dec.More() {
		if err := each(dec); err != nil {
			return err
		}
	}
	return delim(dec, ']')
}

func delim(dec *json.Decoder, want json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok != want {
		return fmt.Errorf("the answer has %v where %v was expected", tok, want)
	}
	return nil
}
