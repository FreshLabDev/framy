// SPDX-License-Identifier: Apache-2.0
package answer

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

func TestBase64(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nsome pixels")
	enc := base64.StdEncoding.EncodeToString(png)
	for name, c := range map[string]struct {
		json      string
		data      []byte
		mediaType string
		bad       bool
	}{
		"bare":            {`"` + enc + `"`, png, "", false},
		"a data URL":      {`"data:image/png;base64,` + enc + `"`, png, "image/png", false},
		"escaped":         {`"` + strings.ReplaceAll(enc, "/", `\/`) + `"`, png, "", false},
		"null":            {`null`, nil, "", false},
		"empty":           {`""`, []byte{}, "", false},
		"not base64":      {`"not*base64"`, nil, "", true},
		"a URL with none": {`"data:image/png;base64"`, nil, "", true},
	} {
		t.Run(name, func(t *testing.T) {
			var b Base64
			if err := json.Unmarshal([]byte(c.json), &b); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if (b.Err != nil) != c.bad {
				t.Fatalf("err %v", b.Err)
			}
			if !bytes.Equal(b.Data, c.data) || b.MediaType != c.mediaType {
				t.Errorf("got %q %q", b.Data, b.MediaType)
			}
		})
	}
	var b Base64
	if err := json.Unmarshal([]byte(`12`), &b); err == nil {
		t.Error("a number taken for base64")
	}
}

type item struct {
	B64 Base64 `json:"b64_json"`
	N   int    `json:"n"`
}

type rest struct {
	Format string `json:"output_format"`
	Usage  *struct {
		Tokens int `json:"tokens"`
	} `json:"usage"`
}

func read(t *testing.T, body string) ([]item, rest, error) {
	t.Helper()
	var items []item
	var r rest
	err := Each(strings.NewReader(body), "data", func(dec *json.Decoder) error {
		var it item
		if err := dec.Decode(&it); err != nil {
			return err
		}
		items = append(items, it)
		return nil
	}, &r)
	return items, r, err
}

func TestEach(t *testing.T) {
	a, b := base64.StdEncoding.EncodeToString([]byte("one")), base64.StdEncoding.EncodeToString([]byte("two"))
	items, r, err := read(t, `{"created":1,"data":[{"b64_json":"`+a+`","n":1},{"b64_json":"`+b+`","n":2,"revised_prompt":"x"}],`+
		`"output_format":"webp","usage":{"tokens":7},"extra":{"nested":[1,2,{"k":"v"}]}}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || string(items[0].B64.Data) != "one" || string(items[1].B64.Data) != "two" || items[1].N != 2 {
		t.Errorf("items %+v", items)
	}
	if r.Format != "webp" || r.Usage == nil || r.Usage.Tokens != 7 {
		t.Errorf("rest %+v", r)
	}

	// The members around the list come in any order, and the list may be
	// null or absent.
	for _, body := range []string{`{"usage":{"tokens":3},"data":null}`, `{"usage":{"tokens":3}}`, `{"data":[],"usage":{"tokens":3}}`} {
		items, r, err := read(t, body)
		if err != nil || len(items) != 0 || r.Usage == nil || r.Usage.Tokens != 3 {
			t.Errorf("%s: %v %+v %+v", body, err, items, r)
		}
	}
	for _, body := range []string{``, `[]`, `{"data":{"b64_json":"x"}}`, `{"data":[{"b64_json":"` + a + `"`, `{"usage":`} {
		if _, _, err := read(t, body); err == nil {
			t.Errorf("%q read as an answer", body)
		}
	}
}

func TestAReaderErrorComesBack(t *testing.T) {
	big := `{"data":[{"b64_json":"` + strings.Repeat("A", 4096) + `"}]}`
	rec := httptest.NewRecorder()
	r := http.MaxBytesReader(rec, io.NopCloser(strings.NewReader(big)), 1024)
	err := Each(r, "data", func(dec *json.Decoder) error {
		var it item
		return dec.Decode(&it)
	}, &rest{})
	var tooLarge *http.MaxBytesError
	if !errors.As(err, &tooLarge) {
		t.Errorf("error %v, want the reader's", err)
	}
}

// An answer of six pictures is read holding about one picture's base64 at a
// time beside the decoded bytes, where decoding it whole allocated over three
// times its size.
func TestAnAnswerIsNotHeldWhole(t *testing.T) {
	const pictures, size = 6, 3 << 20
	var body bytes.Buffer
	body.WriteString(`{"data":[`)
	for i := range pictures {
		if i > 0 {
			body.WriteByte(',')
		}
		pic := bytes.Repeat([]byte{byte(i)}, size)
		body.WriteString(`{"b64_json":"` + base64.StdEncoding.EncodeToString(pic) + `"}`)
	}
	body.WriteString(`],"usage":{"tokens":1}}`)
	answer := body.Bytes()

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	items, _, err := read(t, string(answer))
	runtime.ReadMemStats(&after)
	if err != nil || len(items) != pictures {
		t.Fatalf("%d items, %v", len(items), err)
	}
	// The string copy of the answer that read makes is counted too.
	spent := int64(after.TotalAlloc-before.TotalAlloc) - int64(len(answer)) - pictures*size
	if spent > int64(len(answer)) {
		t.Errorf("reading a %d MiB answer allocated %d MiB beside the pictures", len(answer)>>20, spent>>20)
	}
}
