// Package sourcepos provides an optional goldmark extension that renders
// the source line (and column) of AST nodes as an attribute on the
// corresponding HTML elements. It is meant to let document previewers
// map rendered elements back to the markdown source.
//
// When the extension is enabled, elements produced for headings,
// paragraphs, lists, list items, block quotes, thematic breaks, code
// blocks, links (including autolinks), images and the nodes contributed
// by goldmark's bundled extensions (tables, strikethrough, definition
// lists and footnotes) carry an attribute such as:
//
//	<h1 data-sourcepos="3:1-3:12">Title</h1>
//
// The extension never mutates the AST: attributes are written to the
// output stream only, so rendering the same AST any number of times,
// possibly into different output buffers, produces identical output and
// never accumulates markers.
//
// Nodes without a usable source position (in particular nodes of
// programmatically built ASTs) are rendered unchanged, i.e. without the
// attribute. Raw HTML blocks and inline raw HTML are never touched.
package sourcepos

import (
	"fmt"
	"regexp"
	"sync"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/util"
)

// DefaultAttributeName is the attribute name used when no name is
// configured.
const DefaultAttributeName = "data-sourcepos"

// rendererPriority makes the wrapping renderer functions take precedence
// over the built-in HTML renderer (1000) and the bundled extension
// renderers (500). In goldmark, node renderers with a smaller priority
// value are registered last and therefore win, so the wrappers use a
// value below all built-in renderers.
const rendererPriority = 1

// Config holds options of the source position renderer.
type Config struct {
	// AttributeName is the name of the attribute rendered on elements.
	// It defaults to "data-sourcepos".
	AttributeName string

	// Columns indicates whether the attribute values contain 1-based
	// column numbers in addition to line numbers. When false, only line
	// numbers are rendered ("3-5" instead of "3:1-5:18").
	Columns bool

	// StrictPositions makes rendering fail with an explicit error when a
	// renderable node carries no usable source position. By default such
	// nodes are rendered without the attribute.
	StrictPositions bool

	// extraNodeRenderers are user supplied delegates for custom node kinds.
	extraNodeRenderers []renderer.NodeRenderer
}

// NewConfig returns a new Config with default values.
func NewConfig() Config {
	return Config{
		AttributeName: DefaultAttributeName,
		Columns:       true,
	}
}

// Option is a functional option for the source position extension.
type Option func(*Config)

// WithAttributeName sets the name of the position attribute. The name
// must be a valid HTML attribute name, otherwise rendering fails with an
// explicit error.
func WithAttributeName(name string) Option {
	return func(c *Config) {
		c.AttributeName = name
	}
}

// WithColumns enables or disables column numbers in attribute values.
func WithColumns(enabled bool) Option {
	return func(c *Config) {
		c.Columns = enabled
	}
}

// WithStrictPositions enables or disables failing on nodes without
// usable source positions.
func WithStrictPositions(enabled bool) Option {
	return func(c *Config) {
		c.StrictPositions = enabled
	}
}

// WithNodeRenderers registers additional node renderers whose output
// should be wrapped. Use this when custom AST nodes are rendered by
// third-party node renderers: the given renderers are used as the
// delegate and the position attribute is injected into the first opening
// tag they emit for nodes that have a source position.
func WithNodeRenderers(nrs ...renderer.NodeRenderer) Option {
	return func(c *Config) {
		c.extraNodeRenderers = append(c.extraNodeRenderers, nrs...)
	}
}

var attributeNamePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_.:\-]*$`)

// validate reports an explicit error when the configuration cannot be
// used to emit attributes.
func (c *Config) validate() error {
	if !attributeNamePattern.MatchString(c.AttributeName) {
		return fmt.Errorf("sourcepos: invalid attribute name %q", c.AttributeName)
	}
	return nil
}

// innerRegisterer collects the delegate renderer functions keyed by kind.
type innerRegisterer struct {
	funcs map[ast.NodeKind]renderer.NodeRendererFunc
}

func (r *innerRegisterer) Register(kind ast.NodeKind, f renderer.NodeRendererFunc) {
	// Later registrations win, mirroring renderer.Render registration.
	r.funcs[kind] = f
}

// Renderer is a renderer.NodeRenderer that wraps the standard HTML node
// renderers and injects source position attributes into their output.
type Renderer struct {
	Config

	innerRenderers []renderer.NodeRenderer

	validateOnce sync.Once
	validateErr  error

	indexMu sync.Mutex
	indexes map[indexKey]*lineIndex
}

type indexKey struct {
	ptr uintptr
	len int
}

// NewNodeRenderer returns a renderer.NodeRenderer that injects source
// position attributes into HTML output.
func NewNodeRenderer(opts ...Option) renderer.NodeRenderer {
	cfg := NewConfig()
	for _, opt := range opts {
		opt(&cfg)
	}
	r := &Renderer{
		Config:  cfg,
		indexes: map[indexKey]*lineIndex{},
	}
	r.innerRenderers = append(r.innerRenderers,
		html.NewRenderer(),
		extension.NewTableHTMLRenderer(),
		extension.NewStrikethroughHTMLRenderer(),
		extension.NewDefinitionListHTMLRenderer(),
		extension.NewFootnoteHTMLRenderer(),
	)
	r.innerRenderers = append(r.innerRenderers, cfg.extraNodeRenderers...)
	return r
}

// SetOption implements renderer.SetOptioner. HTML renderer options
// (XHTML, Unsafe, HardWraps and so on) are forwarded to every delegate so
// their output stays identical to the unwrapped renderers.
func (r *Renderer) SetOption(name renderer.OptionName, value any) {
	for _, inner := range r.innerRenderers {
		if setter, ok := inner.(renderer.SetOptioner); ok {
			setter.SetOption(name, value)
		}
	}
}

// noMarkerKinds lists node kinds whose output must be passed through
// untouched:
//   - HTMLBlock and RawHTML emit arbitrary raw markup, where inserting an
//     attribute would alter document content;
//   - Text and String emit character data rather than an element (their
//     renderer may additionally emit <br> for line breaks, which must not
//     be mistaken for the node's own tag).
var noMarkerKinds = map[ast.NodeKind]struct{}{
	ast.KindDocument:  {}, // emits no element; Pos() is always 0
	ast.KindHTMLBlock: {},
	ast.KindRawHTML:   {},
	ast.KindText:      {},
	ast.KindString:    {},
}

// RegisterFuncs implements renderer.NodeRenderer.
func (r *Renderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	inner := &innerRegisterer{funcs: map[ast.NodeKind]renderer.NodeRendererFunc{}}
	for _, nr := range r.innerRenderers {
		nr.RegisterFuncs(inner)
	}

	for kind, f := range inner.funcs {
		delegate := f
		if _, blocked := noMarkerKinds[kind]; blocked {
			reg.Register(kind, delegate)
			continue
		}
		reg.Register(kind, r.wrap(delegate))
	}
}

func (r *Renderer) configError() error {
	r.validateOnce.Do(func() {
		r.validateErr = r.validate()
	})
	return r.validateErr
}

// maxLineIndexEntries bounds the line index cache. Previewers render many
// different sources over the lifetime of a renderer; the cache keeps the
// most recent ones.
const maxLineIndexEntries = 32

func (r *Renderer) lineIndex(source []byte) *lineIndex {
	key := indexKey{ptr: bytesPtr(source), len: len(source)}
	r.indexMu.Lock()
	defer r.indexMu.Unlock()
	if idx, ok := r.indexes[key]; ok {
		return idx
	}
	if len(r.indexes) >= maxLineIndexEntries {
		r.indexes = map[indexKey]*lineIndex{}
	}
	idx := newLineIndex(source)
	r.indexes[key] = idx
	return idx
}

// wrap returns a renderer function that delegates the actual HTML output
// to the standard renderer while injecting the position attribute into
// the opening tag.
func (r *Renderer) wrap(delegate renderer.NodeRendererFunc) renderer.NodeRendererFunc {
	return func(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
		if err := r.configError(); err != nil {
			return ast.WalkStop, err
		}

		var marker []byte
		start, end, hasPos := nodeSpan(n, source)
		if !hasPos {
			if r.StrictPositions {
				return ast.WalkStop, fmt.Errorf(
					"sourcepos: node %s has no source position", n.Kind())
			}
		} else if entering {
			idx := r.lineIndex(source)
			if m, ok := r.formatAttribute(idx, start, end); ok {
				marker = m
			} else if r.StrictPositions {
				return ast.WalkStop, fmt.Errorf(
					"sourcepos: node %s position is out of the source range", n.Kind())
			}
		}

		iw := newInjectWriter(w, marker)
		return delegate(iw, source, n, entering)
	}
}

// New returns a goldmark.Extender enabling source position attributes.
func New(opts ...Option) goldmark.Extender {
	return &sourcePosExtension{opts: opts}
}

// SourcePos is the extension with default options.
var SourcePos goldmark.Extender = New()

type sourcePosExtension struct {
	opts []Option
}

// Extend implements goldmark.Extender.
func (e *sourcePosExtension) Extend(m goldmark.Markdown) {
	m.Renderer().AddOptions(renderer.WithNodeRenderers(
		util.Prioritized(NewNodeRenderer(e.opts...), rendererPriority),
	))
}
