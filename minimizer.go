// Package minimizer is a collage plugin that strips whitespace and comments from
// what an application serves: rendered pages, documents, and mounted assets.
//
//	app, err := collage.New(&collage.Config{
//		Plugins: []collage.Plugin{minimizer.New(minimizer.Options{})},
//		PluginConfig: pluginConfig, // optional; see Options
//	})
//
// It has to be supplied through Config.Plugins rather than RegisterPlugin, because
// it wraps mounted filesystems and that happens while the application is built.
//
// Everything it does is conservative, and deliberately so — see minify.go. It never
// reorders, renames, rewrites or re-encodes anything. If a saving would require
// understanding the grammar rather than recognising where strings and comments
// begin, it is not taken.
package minimizer

import (
	"bytes"
	"context"
	"io/fs"
	"log/slog"
	"path"
	"strings"
	"sync/atomic"

	"github.com/Elagoht/collage/pkg/collage"
)

// Name is the plugin's name, and the key its section of the application's plugin
// configuration is found under.
const Name = "elagoht/minimizer"

// Options selects which formats are minified. The zero value is the default:
// HTML, JSON and CSS are minified and JavaScript is not. JavaScript is off by
// default because it is the format where the difference between a scanner and a
// parser bites hardest; an application that wants it says so.
//
// The application's configuration is decoded over the Options given to New, so a
// section naming only {"js": true} turns JavaScript on and leaves the rest as they
// were.
type Options struct {
	// NoHTML leaves rendered pages, and mounted ".html" files, as they are.
	NoHTML bool `json:"noHTML"`
	// NoJSON leaves documents and mounted files served as JSON, and, in HTML, a
	// <script> holding JSON — structured data, an import map — as they are. JSON
	// minification is exact: encoding/json knows the whole grammar.
	NoJSON bool `json:"noJSON"`
	// NoCSS leaves mounted stylesheets, and <style> elements in HTML, as they are.
	NoCSS bool `json:"noCSS"`
	// JS minifies mounted scripts, and inline <script>s of JavaScript in HTML.
	// A line break is kept wherever a semicolon may stand for it; see minifyJS.
	JS bool `json:"js"`
}

// Plugin is the minifier.
type Plugin struct {
	opts Options
	log  *slog.Logger

	// savedIn and savedOut total the bytes seen and emitted, for the summary
	// logged at shutdown. A minifier that is doing nothing is worth noticing, and
	// a percentage is the only form in which anyone notices. Atomic: pages and
	// documents render concurrently, and every render adds to both.
	savedIn  atomic.Int64
	savedOut atomic.Int64
}

// New returns a minifier configured by opts; the zero Options minifies
// everything except JavaScript.
func New(opts Options) *Plugin { return &Plugin{opts: opts} }

func (p *Plugin) Name() string    { return Name }
func (p *Plugin) Version() string { return "0.2.0" }

// Configure decodes the application's configuration over the Options New was given, and
// registers the filesystem wrapper that minifies mounted assets.
//
// The wrapper is registered unconditionally rather than only when a relevant format
// is enabled, because the configuration decode has already happened by this line
// and a wrapper for a disabled format is a pass-through — one branch per file read,
// against the alternative of a second code path that has to stay in step.
func (p *Plugin) Configure(_ context.Context, host collage.ConfigHost) error {
	p.log = host.Logger()
	var err error
	if p.opts, err = collage.PluginConfig(host, p.opts); err != nil {
		return err
	}
	host.WrapMount(func(inner fs.FS) fs.FS { return &minifyingFS{inner: inner, plugin: p} })
	return nil
}

// Init records the logger for applications that skip Configure — which none can, as
// Configure is what registers the mount wrapper, but a nil logger is not worth
// risking for that reasoning.
func (p *Plugin) Init(_ context.Context, host collage.Host) error {
	if p.log == nil {
		p.log = host.Logger()
	}
	return nil
}

// Shutdown logs what the plugin saved.
func (p *Plugin) Shutdown(context.Context) error {
	in, out := p.savedIn.Load(), p.savedOut.Load()
	if p.log == nil || in == 0 {
		return nil
	}
	p.log.Info("minimizer: stopped",
		"bytesIn", in,
		"bytesOut", out,
		"savedPercent", 100-(out*100/in),
	)
	return nil
}

// OnAfterRender minifies a rendered page.
func (p *Plugin) OnAfterRender(_ context.Context, ev *collage.AfterRenderEvent) error {
	if p.opts.NoHTML {
		return nil
	}
	ev.HTML = p.record(ev.HTML, p.minifyHTML(ev.HTML))
	return nil
}

// OnDocumentRendered minifies a document whose content type it recognises.
//
// The content type is what decides, not the path: a document declares its type and
// the framework serves exactly that, so it is the one description of the body that
// cannot be wrong.
func (p *Plugin) OnDocumentRendered(_ context.Context, ev *collage.DocumentRenderedEvent) error {
	switch {
	case !p.opts.NoJSON && isType(ev.ContentType, "json"):
		ev.Body = p.record(ev.Body, minifyJSON(ev.Body))
	case !p.opts.NoHTML && isType(ev.ContentType, "html"):
		ev.Body = p.record(ev.Body, p.minifyHTML(ev.Body))
	case !p.opts.NoCSS && isType(ev.ContentType, "css"):
		ev.Body = p.record(ev.Body, minifyCSS(ev.Body))
	case p.opts.JS && isType(ev.ContentType, "javascript"):
		ev.Body = p.record(ev.Body, minifyJS(ev.Body))
	}
	return nil
}

// minifyHTML minifies HTML and the inline languages the configuration enables.
func (p *Plugin) minifyHTML(src []byte) []byte {
	return minifyHTMLWith(src, languages{js: p.opts.JS, css: !p.opts.NoCSS, json: !p.opts.NoJSON})
}

// record accounts for a minification and returns whichever of the two to use.
//
// A result larger than the input is discarded. That should not happen, but "should
// not" is not a reason to serve more bytes than arrived, and the check costs a
// comparison.
func (p *Plugin) record(in, out []byte) []byte {
	p.savedIn.Add(int64(len(in)))
	if len(out) >= len(in) {
		p.savedOut.Add(int64(len(in)))
		return in
	}
	p.savedOut.Add(int64(len(out)))
	return out
}

// isType reports whether a content type names a format, ignoring parameters and
// vendor prefixes: "application/rss+xml" is xml, "text/html; charset=utf-8" is html.
func isType(contentType, format string) bool {
	media, _, _ := strings.Cut(contentType, ";")
	media = strings.TrimSpace(strings.ToLower(media))
	return strings.HasSuffix(media, "/"+format) || strings.HasSuffix(media, "+"+format)
}

// minifyingFS minifies a mounted filesystem's files as they are read.
//
// It wraps the filesystem rather than the response because mounts serve through
// http.ServeContent, which brings Range, If-Range and 206 with it. Minifying per
// request would shift every byte offset, so a range request would return the wrong
// slice of a file whose advertised length no longer matched. Minifying here means
// the file *is* the minified one, and ServeContent's arithmetic stays true.
type minifyingFS struct {
	inner  fs.FS
	plugin *Plugin
}

func (m *minifyingFS) Open(name string) (fs.File, error) {
	f, err := m.inner.Open(name)
	if err != nil {
		return nil, err
	}

	minify := m.minifierFor(name)
	if minify == nil {
		return f, nil
	}

	info, err := f.Stat()
	if err != nil || info.IsDir() {
		return f, err
	}

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(f); err != nil {
		f.Close()
		return nil, err
	}
	f.Close()

	return &minifiedFile{
		Reader: bytes.NewReader(m.plugin.record(buf.Bytes(), minify(buf.Bytes()))),
		info:   info,
	}, nil
}

func (m *minifyingFS) minifierFor(name string) func([]byte) []byte {
	switch strings.ToLower(path.Ext(name)) {
	case ".html", ".htm":
		if !m.plugin.opts.NoHTML {
			return m.plugin.minifyHTML
		}
	case ".json":
		if !m.plugin.opts.NoJSON {
			return minifyJSON
		}
	case ".css":
		if !m.plugin.opts.NoCSS {
			return minifyCSS
		}
	case ".js", ".mjs":
		if m.plugin.opts.JS {
			return minifyJS
		}
	}
	return nil
}

// minifiedFile is the minified content presented as an fs.File.
//
// It is a *bytes.Reader, and that is what makes ranges work: the mount hands the
// file to http.ServeContent as an io.ReadSeeker, and ServeContent takes the length
// by seeking to the end rather than from Stat. So the length it advertises is the
// minified length by construction, and every offset it computes is an offset into
// the bytes actually served.
//
// Stat still reports the minified size, but that is correctness for other readers —
// a static build copying the mount, anything calling fs.Stat — rather than what the
// HTTP path depends on. ModTime is kept from the original, so If-Modified-Since
// still behaves.
type minifiedFile struct {
	*bytes.Reader
	info fs.FileInfo
}

func (f *minifiedFile) Stat() (fs.FileInfo, error) {
	return &minifiedInfo{FileInfo: f.info, size: int64(f.Reader.Size())}, nil
}

func (f *minifiedFile) Close() error { return nil }

type minifiedInfo struct {
	fs.FileInfo
	size int64
}

func (i *minifiedInfo) Size() int64 { return i.size }
