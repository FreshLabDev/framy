// SPDX-License-Identifier: Apache-2.0

// Command build makes the site's assets: it bundles internal/web/app into
// one minified script and one stylesheet, copies the fonts and the marks,
// writes the web.* lines of every language, and fills the page template.
// Every asset is named by its content (app.3f9a1c2b4d5e.js), so the server
// gives it a year in the cache; the page itself is checked every time.
//
// Run it from internal/web with go generate. The result goes to dist/ and is
// committed: CI runs it again and fails if anything differs, so the same
// sources always give the same bytes. No Node: esbuild runs as a library.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/evanw/esbuild/pkg/api"

	"github.com/FreshLabDev/framy/internal/fx"
	"github.com/FreshLabDev/framy/internal/i18n"
)

const (
	src  = "app"
	dist = "dist"
	// prefix marks the lines of translations.json that belong to the site.
	prefix = "web."
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "build:", err)
		os.Exit(1)
	}
}

// out collects the files of dist/assets by their published name.
type out struct {
	files map[string][]byte
}

// put stores body under name with its hash: dir/base.<hash>.ext. It returns
// the path the page asks for.
func (o *out) put(name string, body []byte) string {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(filepath.Base(name), ext)
	sum := sha256.Sum256(body)
	hashed := base + "." + hex.EncodeToString(sum[:])[:12] + ext
	o.files[hashed] = body
	return "/assets/" + hashed
}

func run() error {
	o := &out{files: map[string][]byte{}}

	fonts, err := copyDir(o, filepath.Join(src, "fonts"), ".woff2")
	if err != nil {
		return err
	}
	logos, err := inlineDir(filepath.Join(src, "logos"), ".svg", "image/svg+xml")
	if err != nil {
		return err
	}
	texts, err := writeTexts(o)
	if err != nil {
		return err
	}

	css, err := bundle(api.BuildOptions{
		EntryPoints: []string{filepath.Join(src, "app.css")},
		Loader:      map[string]api.Loader{".css": api.LoaderCSS},
		Plugins:     []api.Plugin{assetURLs("woff2", fonts)},
	})
	if err != nil {
		return fmt.Errorf("app.css: %w", err)
	}
	cssURL := o.put("app.css", css)

	logoJS, err := json.Marshal(logos)
	if err != nil {
		return err
	}
	// The settings pick from the family's languages, by their own names, and
	// from the currencies framy prices in: the same lists as the server's.
	var langs [][2]string
	for _, l := range i18n.LANGUAGE_OPTIONS {
		langs = append(langs, [2]string{l.Code, i18n.NameOf(l.Code)})
	}
	js, err := bundle(api.BuildOptions{
		EntryPoints: []string{filepath.Join(src, "src", "main.js")},
		Format:      api.FormatESModule,
		Plugins: []api.Plugin{
			virtual("assets:logos", "export default "+string(logoJS)+";"),
			virtual("assets:lists", "export const LANGS = "+mustJSON(langs)+";\nexport const CURRENCIES = "+mustJSON(fx.Codes)+";"),
		},
	})
	if err != nil {
		return fmt.Errorf("main.js: %w", err)
	}
	jsURL := o.put("app.js", js)

	boot, err := transform(filepath.Join(src, "boot.js"), api.LoaderJS, map[string]string{"TEXTS": mustJSON(texts)})
	if err != nil {
		return err
	}
	skeleton, err := transform(filepath.Join(src, "skeleton.css"), api.LoaderCSS, nil)
	if err != nil {
		return err
	}
	page, err := os.ReadFile(filepath.Join(src, "index.html"))
	if err != nil {
		return err
	}
	// The order of the fills matters: the boot script mentions no placeholder,
	// the template mentions each once.
	html := string(page)
	for _, r := range []struct{ from, to string }{
		{"/*skeleton*/", strings.TrimSpace(skeleton)},
		{"/*boot*/", strings.TrimSpace(boot)},
		{"{{app.css}}", cssURL},
		{"{{app.js}}", jsURL},
	} {
		if strings.Count(html, r.from) != 1 {
			return fmt.Errorf("index.html: want one %s", r.from)
		}
		html = strings.Replace(html, r.from, r.to, 1)
	}

	if err := os.RemoveAll(dist); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dist, "assets"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte(html), 0o644); err != nil {
		return err
	}
	names := make([]string, 0, len(o.files))
	for n := range o.files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dist, "assets", n), o.files[n], 0o644); err != nil {
			return err
		}
	}
	fmt.Printf("build: %s, %s, %d files\n", jsURL, cssURL, len(names))
	return nil
}

// copyDir puts every file of dir with the extension into the assets and
// returns their URLs by base name (openai -> /assets/openai.<hash>.svg).
func copyDir(o *out, dir, ext string) (map[string]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	urls := map[string]string{}
	for _, e := range ents {
		if e.IsDir() || filepath.Ext(e.Name()) != ext {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		urls[strings.TrimSuffix(e.Name(), ext)] = o.put(e.Name(), b)
	}
	return urls, nil
}

// inlineDir returns the files of dir with ext as data URLs, by name. The
// makers' marks are CSS masks: a mask whose file is still loading paints
// nothing, so a mark fetched apart blinks in late or stays empty in a row
// drawn while it loads. All of them together are about 20 KB.
func inlineDir(dir, ext, mime string) (map[string]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	urls := map[string]string{}
	for _, e := range ents {
		if e.IsDir() || filepath.Ext(e.Name()) != ext {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		urls[strings.TrimSuffix(e.Name(), ext)] = "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(b)
	}
	return urls, nil
}

// writeTexts writes the site's lines of each language as its own file, the
// missing ones in English, and returns their URLs by language.
func writeTexts(o *out) (map[string]string, error) {
	raw, err := os.ReadFile(filepath.Join("..", "i18n", "translations.json"))
	if err != nil {
		return nil, err
	}
	var all map[string]map[string]string
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil, err
	}
	urls := map[string]string{}
	for _, l := range i18n.LANGUAGE_OPTIONS {
		lines := map[string]string{}
		for k, by := range all {
			if !strings.HasPrefix(k, prefix) {
				continue
			}
			t := by[l.Code]
			if t == "" {
				t = by[i18n.DefaultLang]
			}
			lines[strings.TrimPrefix(k, prefix)] = t
		}
		if len(lines) == 0 {
			return nil, errors.New("translations.json has no web.* lines")
		}
		urls[l.Code] = o.put("texts-"+l.Code+".json", []byte(mustJSON(lines)))
	}
	return urls, nil
}

// mustJSON is JSON with sorted keys and no HTML escaping, so the bytes are the
// same on every run and "<" stays readable.
func mustJSON(v any) string {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		panic(err)
	}
	return strings.TrimSpace(b.String())
}

// bundle runs esbuild over one entry with the options every asset shares.
func bundle(opt api.BuildOptions) ([]byte, error) {
	opt.Bundle = true
	opt.Write = false
	opt.MinifyWhitespace = true
	opt.MinifyIdentifiers = true
	opt.MinifySyntax = true
	opt.Target = api.ES2022
	opt.Engines = []api.Engine{{Name: api.EngineChrome, Version: "111"}, {Name: api.EngineSafari, Version: "16.4"}, {Name: api.EngineFirefox, Version: "121"}}
	opt.Charset = api.CharsetUTF8
	opt.LegalComments = api.LegalCommentsNone
	opt.LogLevel = api.LogLevelWarning
	opt.Outdir = "out"
	r := api.Build(opt)
	if len(r.Errors) > 0 {
		return nil, buildError(r.Errors)
	}
	for _, f := range r.OutputFiles {
		if strings.HasSuffix(f.Path, ".js") || strings.HasSuffix(f.Path, ".css") {
			return f.Contents, nil
		}
	}
	return nil, errors.New("esbuild wrote nothing")
}

// transform minifies one file that goes inline into the page; defines are
// replaced by their JSON.
func transform(path string, loader api.Loader, define map[string]string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	r := api.Transform(string(b), api.TransformOptions{
		Loader: loader, Define: define, MinifyWhitespace: true, MinifyIdentifiers: true, MinifySyntax: true,
		Target: api.ES2022, Charset: api.CharsetUTF8, LegalComments: api.LegalCommentsNone, Sourcefile: path,
	})
	if len(r.Errors) > 0 {
		return "", buildError(r.Errors)
	}
	return string(r.Code), nil
}

// assetURLs points url(fonts/x.woff2) in the CSS at the hashed copy.
func assetURLs(ext string, urls map[string]string) api.Plugin {
	re := regexp.MustCompile(`([^/\\]+)\.` + ext + `$`)
	return api.Plugin{Name: "asset-urls-" + ext, Setup: func(b api.PluginBuild) {
		b.OnResolve(api.OnResolveOptions{Filter: `\.` + ext + `$`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
			m := re.FindStringSubmatch(a.Path)
			if m == nil || urls[m[1]] == "" {
				return api.OnResolveResult{}, fmt.Errorf("no asset for %s", a.Path)
			}
			return api.OnResolveResult{Path: urls[m[1]], External: true}, nil
		})
	}}
}

// virtual serves a module made here, such as the map of logo URLs.
func virtual(name, code string) api.Plugin {
	return api.Plugin{Name: name, Setup: func(b api.PluginBuild) {
		b.OnResolve(api.OnResolveOptions{Filter: "^" + regexp.QuoteMeta(name) + "$"}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
			return api.OnResolveResult{Path: name, Namespace: "virtual"}, nil
		})
		b.OnLoad(api.OnLoadOptions{Filter: "^" + regexp.QuoteMeta(name) + "$", Namespace: "virtual"}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
			return api.OnLoadResult{Contents: &code, Loader: api.LoaderJS}, nil
		})
	}}
}

func buildError(ms []api.Message) error {
	var b strings.Builder
	for _, m := range ms {
		if m.Location != nil {
			fmt.Fprintf(&b, "%s:%d:%d: ", m.Location.File, m.Location.Line, m.Location.Column)
		}
		b.WriteString(m.Text)
		b.WriteString("\n")
	}
	return errors.New(strings.TrimSpace(b.String()))
}
