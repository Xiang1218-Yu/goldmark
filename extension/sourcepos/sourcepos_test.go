package sourcepos

import (
	"bytes"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer"
	htmlrenderer "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

func newMarkdown(t *testing.T, opts ...Option) goldmark.Markdown {
	t.Helper()
	return goldmark.New(goldmark.WithExtensions(
		append([]goldmark.Extender{New(opts...)}, bundledExtensions()...)...,
	))
}

func newCoreMarkdown(opts ...Option) goldmark.Markdown {
	return goldmark.New(goldmark.WithExtensions(New(opts...)))
}

func bundledExtensions() []goldmark.Extender {
	return []goldmark.Extender{
		extension.Table,
		extension.Strikethrough,
		extension.Linkify,
		extension.TaskList,
		extension.DefinitionList,
		extension.Footnote,
	}
}

func mustConvert(t *testing.T, md goldmark.Markdown, src []byte) string {
	t.Helper()
	var buf bytes.Buffer
	if err := md.Convert(src, &buf); err != nil {
		t.Fatalf("Convert(%q) failed: %v", src, err)
	}
	return buf.String()
}

func assertContains(t *testing.T, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Errorf("output does not contain %q\ngot:\n%s", want, got)
	}
}

// TestCoreNodeMarkers verifies exact line/column markers on headings,
// paragraphs, lists, code blocks, links, images, block quotes and
// thematic breaks, including nested nodes and cross-line text.
func TestCoreNodeMarkers(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"heading", "# Hi\n", `<h1 data-sourcepos="1:1-1:4">Hi</h1>`},
		{"heading level 2", "## T\n", `<h2 data-sourcepos="1:1-1:4">T</h2>`},
		{"empty heading", "#\n", `<h1 data-sourcepos="1:1"></h1>`},
		{"paragraph", "para a\npara b\n", `<p data-sourcepos="1:1-2:6">para a`},
		{"cross line text", "ab\ncd\n", `<p data-sourcepos="1:1-2:2">ab`},
		{"list", "- a\n- b\n",
			`<ul data-sourcepos="1:1-2:3">` + "\n" +
				`<li data-sourcepos="1:1-1:3">a</li>` + "\n" +
				`<li data-sourcepos="2:1-2:3">b</li>`},
		{"nested list", "- a\n  - b\n",
			`<ul data-sourcepos="1:1-2:5">` + "\n" +
				`<li data-sourcepos="1:1-2:5">a` + "\n" +
				`<ul data-sourcepos="2:3-2:5">` + "\n" +
				`<li data-sourcepos="2:3-2:5">b</li>`},
		{"ordered list", "1. first\n2. second\n",
			`<ol data-sourcepos="1:1-2:9">`},
		{"indented code", "    code\n",
			`<pre data-sourcepos="1:5-1:8"><code>code`},
		{"fenced code", "```\nab\n```\n",
			`<pre data-sourcepos="1:1-2:2"><code>ab`},
		{"link", "[l](u)\n",
			`<a data-sourcepos="1:1-1:2" href="u">l</a>`},
		{"autolink", "<http://x.io>\n",
			`<a data-sourcepos="1:1" href="http://x.io">http://x.io</a>`},
		{"image", "![a](u)\n",
			`<img data-sourcepos="1:1-1:3" src="u" alt="a">`},
		{"blockquote", "> q\n",
			`<blockquote data-sourcepos="1:1-1:3">` + "\n" +
				`<p data-sourcepos="1:3">q</p>`},
		{"thematic break xhtml", "---\n", `<hr data-sourcepos="1:1" />`},
		{"code span", "a `x` b\n", `<code data-sourcepos="1:3-1:4">x</code>`},
		{"emphasis", "a *x* b\n", `<em data-sourcepos="1:3-1:4">x</em>`},
		{"hard break", "a  \nb\n", `<br` + `>`},
	}
	mdXHTML := goldmark.New(
		goldmark.WithExtensions(New()),
		goldmark.WithRendererOptions(htmlrenderer.WithXHTML()),
	)
	md := newCoreMarkdown()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mustConvert(t, md, []byte(tc.src))
			if !strings.Contains(got, tc.want) {
				// thematic break exercises the XHTML self-closing path.
				if tc.name == "thematic break xhtml" {
					got = mustConvert(t, mdXHTML, []byte(tc.src))
				}
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("src=%q\nwant substring: %q\ngot:\n%s", tc.src, tc.want, got)
			}
		})
	}
}

// TestExtensionNodes covers GFM tables (including filler empty cells),
// strikethrough, definition lists, footnotes and task list checkboxes.
func TestExtensionNodes(t *testing.T) {
	md := newMarkdown(t)

	table := mustConvert(t, md, []byte("| A | B |\n|---|---|\n| x |\n"))
	assertContains(t, table, `<table data-sourcepos="1:1`)
	assertContains(t, table, `<thead data-sourcepos="1:1`)
	assertContains(t, table, `<th data-sourcepos="1:2-1:3">A</th>`)
	assertContains(t, table, `<th data-sourcepos="1:6-1:7">B</th>`)
	assertContains(t, table, `<tr data-sourcepos="3:1-3:3">`)
	assertContains(t, table, `<td data-sourcepos="3:2-3:3">x</td>`)
	// The filler cell for the missing second column has no position and
	// must be rendered unchanged.
	assertContains(t, table, `<td></td>`)

	strike := mustConvert(t, md, []byte("a ~~x~~ b\n"))
	assertContains(t, strike, `<del data-sourcepos="1:3-1:5">x</del>`)

	dl := mustConvert(t, md, []byte("term\n:   desc text\n"))
	assertContains(t, dl, `<dl data-sourcepos="1:1-2:13">`)
	assertContains(t, dl, `<dt data-sourcepos="1:1-1:4">term</dt>`)
	assertContains(t, dl, `<dd data-sourcepos="2:1-2:13">desc text</dd>`)

	footnote := mustConvert(t, md, []byte("See note[^1].\n\n[^1]: note text\n"))
	assertContains(t, footnote, `<li data-sourcepos="3:1-3:15" id="fn:1">`)
	assertContains(t, footnote, `<div data-sourcepos="3:7-3:15" class="footnotes"`)
	// The in-text footnote reference corresponds to source text and is
	// located on the wrapping <sup>; the generated backlink must not be
	// marked.
	assertContains(t, footnote, `<sup data-sourcepos="1:9" id="fnref:1">`)
	assertContains(t, footnote, `<a href="#fn:1" class="footnote-ref"`)
	if strings.Contains(footnote, `class="footnote-backref" role="doc-backlink" data-sourcepos`) ||
		strings.Contains(footnote, `<a data-sourcepos`+"\n") {
		t.Errorf("generated backlink must not be marked:\n%s", footnote)
	}

	task := mustConvert(t, md, []byte("- [ ] task\n"))
	assertContains(t, task, `<li data-sourcepos="1:1-1:10">`)
	assertContains(t, task, `<input disabled="" type="checkbox"> task`)
	if strings.Contains(task, `<input data-sourcepos`) {
		t.Errorf("checkbox must not be marked:\n%s", task)
	}
}

// TestLineOnlyMarkers checks the WithColumns option.
func TestLineOnlyMarkers(t *testing.T) {
	md := newCoreMarkdown(WithColumns(false))
	got := mustConvert(t, md, []byte("# Hi\n"))
	assertContains(t, got, `<h1 data-sourcepos="1">Hi</h1>`)

	multi := mustConvert(t, md, []byte("a\nb\n"))
	assertContains(t, multi, `<p data-sourcepos="1-2">`)
}

// TestCustomAttributeName checks the configurable attribute name and
// explicit handling of invalid names.
func TestCustomAttributeName(t *testing.T) {
	md := newCoreMarkdown(WithAttributeName("data-line"))
	got := mustConvert(t, md, []byte("# Hi\n"))
	assertContains(t, got, `<h1 data-line="1:1-1:4">Hi</h1>`)

	for _, bad := range []string{"", "bad name", "a>b", `a"b`, "1abc"} {
		md := newCoreMarkdown(WithAttributeName(bad))
		var buf bytes.Buffer
		err := md.Convert([]byte("# Hi\n"), &buf)
		if err == nil {
			t.Errorf("expected error for invalid attribute name %q, got output:\n%s", bad, buf.String())
		} else if !strings.Contains(err.Error(), "invalid attribute name") {
			t.Errorf("error for %q should mention invalid attribute name, got %v", bad, err)
		}
	}
}

// TestProgrammaticAST renders an AST built without source positions: no
// markers are emitted, rendering succeeds and output stays standard.
func TestProgrammaticAST(t *testing.T) {
	md := newMarkdown(t)
	doc := ast.NewDocument()
	p := ast.NewParagraph()
	p.AppendChild(p, ast.NewString([]byte("hello ")))
	em := ast.NewEmphasis(1)
	em.AppendChild(em, ast.NewString([]byte("x")))
	p.AppendChild(p, em)
	doc.AppendChild(doc, p)

	var buf bytes.Buffer
	if err := md.Renderer().Render(&buf, []byte(""), doc); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	want := "<p>hello <em>x</em></p>\n"
	if got != want {
		t.Errorf("programmatic AST output mismatch:\nwant %q\ngot  %q", want, got)
	}
	if strings.Contains(got, "data-sourcepos") {
		t.Errorf("programmatic AST must not carry markers:\n%s", got)
	}
}

// TestMixedProgrammaticNode inserts a position-less node into a parsed
// document: other nodes keep markers, the synthetic one does not.
func TestMixedProgrammaticNode(t *testing.T) {
	md := newCoreMarkdown()
	doc := md.Parser().Parse(text.NewReader([]byte("# T\n")))
	p := ast.NewParagraph()
	p.AppendChild(p, ast.NewString([]byte("synthetic")))
	doc.AppendChild(doc, p)

	var buf bytes.Buffer
	if err := md.Renderer().Render(&buf, []byte("# T\n"), doc); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	assertContains(t, got, `<h1 data-sourcepos="1:1-1:3">T</h1>`)
	assertContains(t, got, `<p>synthetic</p>`)
	if strings.Contains(strings.SplitN(got, "synthetic", 2)[1], "data-sourcepos") {
		t.Errorf("synthetic node must not carry markers:\n%s", got)
	}
}

// TestStrictPositions verifies the explicit error handling when nodes
// lack necessary positions.
func TestStrictPositions(t *testing.T) {
	md := newCoreMarkdown(WithStrictPositions(true))
	doc := ast.NewDocument()
	p := ast.NewParagraph()
	p.AppendChild(p, ast.NewString([]byte("x")))
	doc.AppendChild(doc, p)

	var buf bytes.Buffer
	err := md.Renderer().Render(&buf, []byte(""), doc)
	if err == nil {
		t.Fatalf("expected error for position-less node, got:\n%s", buf.String())
	}
	if !strings.Contains(err.Error(), "no source position") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestRepeatRenderAndBuffers renders the same AST repeatedly into one
// buffer reuse pattern and into different buffers: markers must neither
// accumulate nor get misplaced.
func TestRepeatRenderAndBuffers(t *testing.T) {
	src := []byte("# T\n\n- a\n- b\n\n```\nx\n```\n")
	md := newMarkdown(t)
	doc := md.Parser().Parse(text.NewReader(src))

	var first string
	for i := 0; i < 3; i++ {
		var buf bytes.Buffer
		if err := md.Renderer().Render(&buf, src, doc); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = buf.String()
		} else if buf.String() != first {
			t.Fatalf("render %d differs from render 0:\n%s\n---\n%s", i, first, buf.String())
		}
		if n := strings.Count(buf.String(), "data-sourcepos"); n != strings.Count(first, "data-sourcepos") {
			t.Fatalf("marker count changed on repeated render: %d", n)
		}
	}

	// The same AST rendered concurrently into independent buffers.
	var wg sync.WaitGroup
	outputs := make([][]byte, 8)
	errs := make(chan error, len(outputs))
	for i := range outputs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var buf bytes.Buffer
			if err := md.Renderer().Render(&buf, src, doc); err != nil {
				errs <- err
				return
			}
			outputs[i] = buf.Bytes()
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	for _, out := range outputs[1:] {
		if !bytes.Equal(out, outputs[0]) {
			t.Fatalf("concurrent render into different buffer differs:\n%s\n---\n%s", outputs[0], out)
		}
	}
}

// TestRawHTMLUntouched ensures arbitrary raw markup is never modified.
func TestRawHTMLUntouched(t *testing.T) {
	md := goldmark.New(
		goldmark.WithExtensions(New()),
		goldmark.WithRendererOptions(htmlrenderer.WithUnsafe()),
	)
	src := []byte("<div title=\"x\">\nraw <b>bold</b>\n</div>\n\na <span>inline</span> b\n")
	got := mustConvert(t, md, src)
	if strings.Contains(got, "<div data-sourcepos") || strings.Contains(got, "<b data-sourcepos") ||
		strings.Contains(got, "<span data-sourcepos") {
		t.Fatalf("raw markup must not be annotated:\n%s", got)
	}
	assertContains(t, got, "<div title=\"x\">\nraw <b>bold</b>\n</div>")
	assertContains(t, got, "a <span>inline</span> b")
}

// TestSubtreeRender renders an AST subtree directly (the walk does not
// start at Document); markers must still resolve and nothing may leak.
func TestSubtreeRender(t *testing.T) {
	src := []byte("hello world\n")
	md := newCoreMarkdown()
	doc := md.Parser().Parse(text.NewReader(src))
	p := doc.FirstChild().(*ast.Paragraph)

	var buf bytes.Buffer
	if err := md.Renderer().Render(&buf, src, p); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	want := `<p data-sourcepos="1:1-1:11">hello world</p>` + "\n"
	if got != want {
		t.Fatalf("subtree render mismatch:\nwant %q\ngot  %q", want, got)
	}
}

// markerPattern strips position attributes for output comparison.
var markerPattern = regexp.MustCompile(` data-sourcepos="[^"]*"`)

// TestOutputIdenticalExceptMarkers compares stock goldmark output with
// sourcepos-enabled output after stripping the added attribute, in both
// HTML5 and XHTML modes.
func TestOutputIdenticalExceptMarkers(t *testing.T) {
	src := []byte(strings.Join([]string{
		"# Heading",
		"",
		"A paragraph with `code`, *em*, [link](http://example.com/p?a=1&b=2),",
		"a soft wrapped line and an autolink <http://example.com>.",
		"",
		"- item one",
		"  1. nested",
		"  2. items",
		"- [x] task",
		"",
		"> quoted",
		"> text",
		"",
		"---",
		"",
		"```go",
		"func f() {}",
		"```",
		"",
		"| A | B |",
		"|---|---:|",
		"| 1 | 2 |",
		"",
		"~~done~~",
		"",
		"term",
		":   description",
		"",
		"note[^a]",
		"",
		"[^a]: footnote body",
		"",
		"<div class=\"raw\">raw <i>html</i></div>",
		"",
	}, "\n"))

	exts := bundledExtensions()
	for _, xhtml := range []bool{false, true} {
		name := "HTML5"
		var rendererOpts []goldmark.Option
		if xhtml {
			name = "XHTML"
			rendererOpts = append(rendererOpts, goldmark.WithRendererOptions(htmlrenderer.WithXHTML()))
		}
		t.Run(name, func(t *testing.T) {
			stockOpts := append([]goldmark.Option{
				goldmark.WithExtensions(exts...),
				goldmark.WithRendererOptions(htmlrenderer.WithUnsafe()),
			}, rendererOpts...)
			stock := goldmark.New(stockOpts...)
			var want bytes.Buffer
			if err := stock.Convert(src, &want); err != nil {
				t.Fatal(err)
			}

			posOpts := append([]goldmark.Option{
				goldmark.WithExtensions(append([]goldmark.Extender{New()}, exts...)...),
				goldmark.WithRendererOptions(htmlrenderer.WithUnsafe()),
			}, rendererOpts...)
			withPos := goldmark.New(posOpts...)
			var marked bytes.Buffer
			if err := withPos.Convert(src, &marked); err != nil {
				t.Fatal(err)
			}

			stripped := markerPattern.ReplaceAll(marked.Bytes(), nil)
			if !bytes.Equal(stripped, want.Bytes()) {
				t.Fatalf("output differs beyond position attributes\n--- stock ---\n%s\n--- stripped ---\n%s",
					want.String(), string(stripped))
			}
		})
	}
}

// TestExtensionDisabledOutputUnchanged proves that not enabling the
// extension leaves the default renderer output untouched.
func TestExtensionDisabledOutputUnchanged(t *testing.T) {
	src := []byte("# T\n\n[a](u)\n")
	plain := goldmark.New()
	got := mustConvert(t, plain, src)
	if strings.Contains(got, "data-sourcepos") {
		t.Fatalf("default output must not contain markers:\n%s", got)
	}
}

// kindWidget is a custom AST node kind used by the custom renderer test.
var kindWidget = ast.NewNodeKind("Widget")

type widget struct {
	ast.BaseBlock
}

func (w *widget) Kind() ast.NodeKind { return kindWidget }
func (w *widget) Dump(source []byte, level int) {
	ast.DumpHelper(w, source, level, nil, nil)
}

type widgetRenderer struct{}

func (widgetRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(kindWidget, func(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			_, _ = w.WriteString("<widget>")
			for i := 0; i < n.Lines().Len(); i++ {
				seg := n.Lines().At(i)
				_, _ = w.Write(seg.Value(source))
			}
		} else {
			_, _ = w.WriteString("</widget>")
		}
		return ast.WalkContinue, nil
	})
}

// TestCustomNodeRenderer verifies that third-party node renderers can be
// wrapped through WithNodeRenderers.
func TestCustomNodeRenderer(t *testing.T) {
	source := []byte("abc\n")
	md := newCoreMarkdown(WithNodeRenderers(widgetRenderer{}))
	w := &widget{}
	w.SetPos(0)
	w.Lines().Append(text.NewSegment(0, 3))
	doc := ast.NewDocument()
	doc.AppendChild(doc, w)

	var buf bytes.Buffer
	if err := md.Renderer().Render(&buf, source, doc); err != nil {
		t.Fatal(err)
	}
	want := `<widget data-sourcepos="1:1-1:3">abc</widget>`
	if strings.TrimSpace(buf.String()) != want {
		t.Fatalf("custom node marker mismatch:\nwant %q\ngot  %q", want, buf.String())
	}
}

// TestConcurrentConversions stresses shared renderer use from multiple
// goroutines with both shared and distinct sources.
func TestConcurrentConversions(t *testing.T) {
	md := newMarkdown(t)
	var wg sync.WaitGroup
	errs := make(chan error, 200)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			src := []byte("# Title\n\ntext with [l](u) and ~~s~~\n\n- a\n- b\n")
			if i%2 == 0 {
				src = append(src, []byte("\n\n| x |\n|---|\n| 1 |\n")...)
			}
			var buf bytes.Buffer
			if err := md.Convert(src, &buf); err != nil {
				errs <- err
				return
			}
			if !strings.Contains(buf.String(), "data-sourcepos") {
				errs <- markerError("output without markers")
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

type markerError string

func (e markerError) Error() string { return string(e) }
