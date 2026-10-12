package publish_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/publish"
)

func newMarkdown() goldmark.Markdown {
	return goldmark.New(
		goldmark.WithExtensions(extension.Footnote),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	)
}

const docSource = `# Title

intro paragraph

# Alpha

Alpha body with [a link][r1] and a note[^a].

## Alpha One

nested section content with [^b].

### Deep One

deepest content.

## Alpha Two

second nested.

[^a]: alpha note that also points at [^c].

# Beta

Beta body.

[r1]: https://example.com/a "A"
[r2]: https://example.com/b
[^b]: bravo note
[^c]: charlie note
`

func mustExtract(t *testing.T, p *publish.Publisher, src []byte, sel publish.Selector) *publish.Chapter {
	t.Helper()
	ch, err := p.Extract(src, sel)
	if err != nil {
		t.Fatalf("Extract(%+v): unexpected error: %v", sel, err)
	}
	return ch
}

func TestExtractByTextIncludesNestedSectionsAndStopsAtBoundary(t *testing.T) {
	p := publish.New(newMarkdown())
	ch := mustExtract(t, p, []byte(docSource), publish.Selector{Text: "Alpha"})

	if ch.Level != 1 || ch.Text != "Alpha" || ch.ID != "alpha" || ch.Key != "alpha" {
		t.Fatalf("unexpected chapter metadata: %+v", ch)
	}
	md := string(ch.Markdown)
	for _, want := range []string{"# Alpha", "## Alpha One", "### Deep One", "## Alpha Two"} {
		if !strings.Contains(md, want) {
			t.Errorf("fragment missing %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "# Beta") {
		t.Errorf("fragment must not contain the following section Beta:\n%s", md)
	}
	if strings.Contains(md, "intro paragraph") {
		t.Errorf("fragment must not contain content before the heading:\n%s", md)
	}
}

func TestExtractByAutoIDAndCustomID(t *testing.T) {
	p := publish.New(newMarkdown())
	ch := mustExtract(t, p, []byte(docSource), publish.Selector{ID: "alpha-one"})
	if ch.Text != "Alpha One" || ch.Level != 2 {
		t.Fatalf("unexpected chapter: %+v", ch)
	}

	md := goldmark.New(
		goldmark.WithExtensions(extension.Footnote),
		goldmark.WithParserOptions(parser.WithHeadingAttribute()),
	)
	src := []byte("# Hello {#custom-id}\n\nbody\n")
	ch2, err := publish.New(md).Extract(src, publish.Selector{ID: "custom-id"})
	if err != nil {
		t.Fatalf("extract by custom id: %v", err)
	}
	if ch2.ID != "custom-id" || ch2.Text != "Hello" {
		t.Fatalf("custom id chapter: %+v", ch2)
	}
}

func TestExtractByTextMatchesPlainHeadingText(t *testing.T) {
	src := []byte("# A *fancy* `code` Heading\n\nx\n")
	p := publish.New(goldmark.New(goldmark.WithParserOptions(parser.WithAutoHeadingID())))
	ch := mustExtract(t, p, src, publish.Selector{Text: "A fancy code Heading"})
	if ch.Key != "a-fancy-code-heading" {
		t.Fatalf("unexpected key %q", ch.Key)
	}
}

func TestSelectorValidation(t *testing.T) {
	p := publish.New(newMarkdown())
	for _, sel := range []publish.Selector{
		{},
		{ID: "a", Text: "b"},
	} {
		if _, err := p.Extract(nil, sel); !errors.Is(err, publish.ErrSelectorEmpty) {
			t.Errorf("selector %+v: want ErrSelectorEmpty, got %v", sel, err)
		}
	}
	if _, err := p.Extract([]byte(docSource), publish.Selector{Text: "Nope"}); !errors.Is(err, publish.ErrHeadingNotFound) {
		t.Fatalf("want ErrHeadingNotFound, got %v", err)
	}
	src := []byte("# Dup\n\na\n\n# Dup\n\nb\n")
	if _, err := p.Extract(src, publish.Selector{Text: "Dup"}); !errors.Is(err, publish.ErrAmbiguousHeading) {
		t.Fatalf("want ErrAmbiguousHeading, got %v", err)
	}
}

func TestExtractSetextHeading(t *testing.T) {
	src := []byte("Before\n\nChapter\n=======\n\nbody text\n\nNested\n------\n\nnested body\n\nAfter\n=====\n\nx\n")
	p := publish.New(goldmark.New())
	ch := mustExtract(t, p, src, publish.Selector{Text: "Chapter"})
	md := string(ch.Markdown)
	if !strings.HasPrefix(md, "Chapter\n=======") {
		t.Fatalf("setext section slice wrong:\n%q", md)
	}
	if !strings.Contains(md, "Nested\n------") {
		t.Fatalf("deeper setext subsection must be included:\n%q", md)
	}
	if strings.Contains(md, "After") {
		t.Fatalf("level-1 setext boundary ignored:\n%q", md)
	}
}

func TestFootnotesBroughtFromOutsideSection(t *testing.T) {
	src := []byte("# Alpha\n\nuses [^a] and [^b].\n\n## A1\n\nuses [^a] again.\n\n# Beta\n\nx\n\n[^a]: note a\n\n[^b]: note b\n")
	p := publish.New(newMarkdown())
	ch := mustExtract(t, p, []byte(src), publish.Selector{Text: "Alpha"})

	if got := strings.Join(ch.Footnotes, ","); got != "a,b" {
		t.Fatalf("Footnotes = %q, want a,b", got)
	}
	md := string(ch.Markdown)
	if strings.Count(md, "[^a]: note a") != 1 || strings.Count(md, "[^b]: note b") != 1 {
		t.Fatalf("external definitions must be attached exactly once:\n%s", md)
	}

	html := mustRender(t, p, ch)
	if strings.Count(string(html), `class="footnote-ref"`) != 3 {
		t.Errorf("want 3 footnote references (two unique, one repeated), got:\n%s", html)
	}
	// Chapter-local numbering starts at 1.
	if !strings.Contains(string(html), `href="#fn:1"`) || !strings.Contains(string(html), `href="#fn:2"`) {
		t.Errorf("want locally numbered footnotes 1 and 2:\n%s", html)
	}
	if strings.Contains(string(html), "note b") == false {
		t.Errorf("footnote body missing:\n%s", html)
	}
}

func TestFootnoteDefinitionInsideSectionNotDuplicated(t *testing.T) {
	src := []byte("# Alpha\n\nuses [^a].\n\n[^a]: note a\n\n# Beta\n\nbeta [^a]\n")
	p := publish.New(newMarkdown())
	ch := mustExtract(t, p, []byte(src), publish.Selector{Text: "Alpha"})
	if strings.Count(string(ch.Markdown), "[^a]:") != 1 {
		t.Fatalf("definition duplicated or missing:\n%s", ch.Markdown)
	}
	if len(ch.ReferenceDefinitions) != 0 {
		t.Fatalf("unexpected refs: %v", ch.ReferenceDefinitions)
	}
}

func TestTransitiveFootnoteDependencies(t *testing.T) {
	src := []byte("# Alpha\n\nuses [^a].\n\n# Beta\n\nx\n\n[^a]: a points to [^b]\n[^b]: b points to [^c]\n[^c]: final\n")
	p := publish.New(newMarkdown())
	ch := mustExtract(t, p, []byte(src), publish.Selector{Text: "Alpha"})
	got := strings.Join(ch.Footnotes, ",")
	if got != "a,b,c" {
		t.Fatalf("transitive footnotes = %q, want a,b,c", got)
	}
	for _, note := range []string{"a points to", "b points to", "final"} {
		if !strings.Contains(string(mustRender(t, p, ch)), note) {
			t.Fatalf("rendered chapter missing %q", note)
		}
	}
}

func TestFootnoteDefinedBeforeSection(t *testing.T) {
	src := []byte("[^a]: note defined first\n\n# Alpha\n\nuses [^a].\n\n# Beta\n\nx\n")
	p := publish.New(newMarkdown())
	ch := mustExtract(t, p, []byte(src), publish.Selector{Text: "Alpha"})
	if strings.Join(ch.Footnotes, ",") != "a" {
		t.Fatalf("footnotes = %v", ch.Footnotes)
	}
	if !strings.Contains(string(ch.Markdown), "[^a]: note defined first") {
		t.Fatalf("pre-section definition not attached:\n%s", ch.Markdown)
	}
	if !strings.Contains(string(mustRender(t, p, ch)), "note defined first") {
		t.Fatal("pre-section footnote did not render")
	}
}

func TestRepublishByIDSolvesHeadingRename(t *testing.T) {
	// An explicit {#id} attribute stays attached to a chapter even after the
	// heading text is edited in the source document, so the region keeps
	// matching across republish.
	md := goldmark.New(
		goldmark.WithExtensions(extension.Footnote),
		goldmark.WithParserOptions(parser.WithAutoHeadingID(), parser.WithHeadingAttribute()),
	)
	p := publish.New(md)
	out, _, err := p.Publish([]byte("# Old Name {#chapter-slug}\n\nv1\n"), publish.Selector{ID: "chapter-slug"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	out2, _, err := p.Publish([]byte("# New Name {#chapter-slug}\n\nv2\n"), publish.Selector{ID: "chapter-slug"}, out)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out2)
	if !strings.Contains(s, "New Name") || strings.Contains(s, ">Old Name<") {
		t.Fatalf("region not updated after rename:\n%s", s)
	}
	if strings.Count(s, "begin:chapter-slug") != 1 {
		t.Fatalf("rename created a second region:\n%s", s)
	}
}

func TestIncompleteFootnoteRejected(t *testing.T) {
	src := []byte("# Alpha\n\nbody with [^ghost].\n\n# Beta\n\nx\n")
	p := publish.New(newMarkdown())
	_, err := p.Extract([]byte(src), publish.Selector{Text: "Alpha"})
	if !errors.Is(err, publish.ErrIncompleteFootnotes) {
		t.Fatalf("want ErrIncompleteFootnotes, got %v", err)
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("error should name the missing label: %v", err)
	}

	// Publish must not commit anything on the failure.
	target := []byte("manual target\n")
	out, rec, err := p.Publish([]byte(src), publish.Selector{Text: "Alpha"}, target)
	if err == nil || rec != nil || out != nil {
		t.Fatalf("Publish returned (%q, %v, %v), want error and no commit", out, rec, err)
	}
	if loaded, lerr := p.Store().Load("alpha"); !errors.Is(lerr, publish.ErrRecordNotFound) {
		t.Fatalf("record store should be untouched, got record=%v err=%v", loaded, lerr)
	}
}

func TestFootnoteLookalikesAreNotReferences(t *testing.T) {
	src := []byte("# Alpha\n\ninline code `[^x]` and escaped \\[^y] and fence:\n\n```\n[^z]\n```\n\nliteral definition follows\n\n[^w]: note w\n")
	p := publish.New(newMarkdown())
	ch := mustExtract(t, p, []byte(src), publish.Selector{Text: "Alpha"})
	if len(ch.Footnotes) != 0 {
		t.Fatalf("unexpected footnotes: %v", ch.Footnotes)
	}
}

func TestExternalLinkReferenceDefinitionsAttachedOnlyWhenUsed(t *testing.T) {
	p := publish.New(newMarkdown())
	ch := mustExtract(t, p, []byte(docSource), publish.Selector{Text: "Alpha"})
	if got := strings.Join(ch.ReferenceDefinitions, ","); got != "r1" {
		t.Fatalf("ReferenceDefinitions = %q, want r1 only", got)
	}
	md := string(ch.Markdown)
	if !strings.Contains(md, "[r1]: https://example.com/a") {
		t.Fatalf("used external definition missing from fragment:\n%s", md)
	}
	if strings.Contains(md, "[r2]:") {
		t.Fatalf("unused external definition must not be attached:\n%s", md)
	}
	html := string(mustRender(t, p, ch))
	if !strings.Contains(html, `href="https://example.com/a"`) {
		t.Fatalf("reference link must resolve in chapter render:\n%s", html)
	}
}

func TestReferenceDefinitionInsideSectionUsed(t *testing.T) {
	src := []byte("# Alpha\n\nsee [local][lo].\n\n[lo]: https://local.example\n\n# Beta\n\nx\n")
	p := publish.New(newMarkdown())
	ch := mustExtract(t, p, []byte(src), publish.Selector{Text: "Alpha"})
	if len(ch.ReferenceDefinitions) != 0 {
		t.Fatalf("internal definition listed as external: %v", ch.ReferenceDefinitions)
	}
	if !strings.Contains(string(mustRender(t, p, ch)), "https://local.example") {
		t.Fatal("internal reference did not resolve")
	}
}

func TestIndependentASTIsWalkableAndRenderableStandalone(t *testing.T) {
	md := newMarkdown()
	p := publish.New(md)
	ch := mustExtract(t, p, []byte(docSource), publish.Selector{Text: "Alpha"})

	// The fragment AST is owned by the chapter: walking it independently must
	// find the heading, the nested subsection headings, footnote links and
	// never reference the source document.
	var headings, footnoteLinks int
	err := ast.Walk(ch.AST, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n.(type) {
		case *ast.Heading:
			headings++
		case *east.FootnoteLink:
			footnoteLinks++
		}
		return ast.WalkContinue, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if headings != 4 { // Alpha + Alpha One + Deep One + Alpha Two
		t.Errorf("walk headings = %d, want 4", headings)
	}
	if footnoteLinks != 3 { // [^a], [^b] and the transitive [^c] inside [^a]
		t.Errorf("walk footnote links = %d, want 3", footnoteLinks)
	}
	if ch.Heading == nil || ch.Heading.Level != 1 || string(ch.Heading.Text(ch.Markdown)) != "Alpha" {
		t.Fatalf("chapter heading not available in fragment AST: %+v", ch.Heading)
	}
	if ch.AST.OwnerDocument() != ch.AST {
		t.Fatal("fragment document is not the owner document of its nodes")
	}

	// A second, identically configured goldmark instance can render the
	// fragment bytes on its own.
	first := mustRender(t, p, ch)
	second := publish.New(newMarkdown())
	var buf bytes.Buffer
	if err := second.Markdown().Renderer().Render(&buf, ch.Markdown, ch.AST); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(first), bytes.TrimSpace(buf.Bytes())) {
		t.Fatalf("standalone render differs:\n%s\n----\n%s", first, buf.String())
	}
}

func TestRenderGoesThroughGoldmarkRendererChain(t *testing.T) {
	p := publish.New(newMarkdown())
	ch := mustExtract(t, p, []byte(docSource), publish.Selector{Text: "Alpha"})
	html := string(mustRender(t, p, ch))
	if !strings.Contains(html, "<h1 ") || !strings.Contains(html, "<h2 ") || !strings.Contains(html, "<h3 ") {
		t.Fatalf("chapter is not rendered as standard goldmark HTML:\n%s", html)
	}
	if !strings.Contains(html, `role="doc-endnotes"`) {
		t.Fatalf("footnote extension renderer did not run:\n%s", html)
	}
}

func TestPublishAppendsRegionAndPreservesManualContent(t *testing.T) {
	p := publish.New(newMarkdown())
	target := []byte("<html><body>\n<p>manual intro</p>\n")
	out, rec, err := p.Publish([]byte(docSource), publish.Selector{Text: "Alpha"}, target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), string(target)) {
		t.Fatalf("manual content prefix not preserved:\n%s", out)
	}
	wantBegin := "<!-- goldmark:chapter:begin:alpha -->"
	wantEnd := "<!-- goldmark:chapter:end:alpha -->"
	if !strings.Contains(string(out), wantBegin) || !strings.Contains(string(out), wantEnd) {
		t.Fatalf("markers missing:\n%s", out)
	}
	if rec.Key != "alpha" || rec.ChapterText != "Alpha" || rec.HeadingLevel != 1 {
		t.Fatalf("bad record metadata: %+v", rec)
	}
	if rec.GeneratedHTML == "" || !strings.Contains(rec.GeneratedHTML, "Alpha One") {
		t.Fatal("record must store the last generated result")
	}
	if rec.SourceVersion == "" || len(rec.SourceVersion) != 64 {
		t.Fatalf("bad source version %q", rec.SourceVersion)
	}
	if strings.TrimSpace(rec.GeneratedHTML) != strings.TrimSpace(regionBody(out, "alpha")) {
		t.Fatal("recorded generated HTML differs from merged region")
	}
}

func TestPublishRepublishReplacesOnlyGeneratedRegion(t *testing.T) {
	p := publish.New(newMarkdown())
	src1 := []byte("# Alpha\n\nfirst version [^a].\n\n[^a]: note one\n")
	src2 := []byte("# Alpha\n\nsecond version.\n")

	firstTarget := []byte("HEAD-MANUAL\n")
	out, rec1, err := p.Publish(src1, publish.Selector{Text: "Alpha"}, firstTarget)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate manual edits around the target: add manual content after the
	// region.
	out = append(out, []byte("<p>manual footer</p>\n")...)

	out2, rec2, err := p.Publish(src2, publish.Selector{Text: "Alpha"}, out)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out2)
	if !strings.HasPrefix(s, "HEAD-MANUAL\n") || !strings.Contains(s, "<p>manual footer</p>") {
		t.Fatalf("manual content outside region was modified:\n%s", s)
	}
	if !strings.Contains(s, "second version") {
		t.Fatal("new generated content missing")
	}
	if strings.Contains(s, "first version") || strings.Contains(s, "note one") {
		t.Fatal("old generated content was not replaced:\n" + s)
	}
	if rec1.SourceVersion == rec2.SourceVersion {
		t.Fatal("source version did not update after source change")
	}
	if rec2.SourceVersion != mustRecord(t, p, "alpha").SourceVersion {
		t.Fatal("record store did not commit the new version")
	}
}

func TestMultipleChaptersCoexistAndDoNotOverwriteEachOther(t *testing.T) {
	p := publish.New(newMarkdown())
	out, _, err := p.Publish([]byte(docSource), publish.Selector{Text: "Alpha"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	out, _, err = p.Publish([]byte(docSource), publish.Selector{Text: "Beta"}, out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(out), "goldmark:chapter:begin:alpha") != 1 ||
		strings.Count(string(out), "goldmark:chapter:begin:beta") != 1 {
		t.Fatalf("expected both regions exactly once:\n%s", out)
	}

	// Re-publishing Alpha leaves Beta's region byte-identical.
	betaBefore := regionBody(out, "beta")
	changed := []byte(strings.ReplaceAll(docSource, "Alpha body", "ALPHA UPDATED BODY"))
	out2, _, err := p.Publish(changed, publish.Selector{Text: "Alpha"}, out)
	if err != nil {
		t.Fatal(err)
	}
	if regionBody(out2, "beta") != betaBefore {
		t.Fatal("beta region changed while publishing alpha")
	}
	if !strings.Contains(string(out2), "ALPHA UPDATED BODY") {
		t.Fatal("alpha region was not updated")
	}
}

func TestRepublishIsIdempotent(t *testing.T) {
	p := publish.New(newMarkdown())
	out1, rec1, err := p.Publish([]byte(docSource), publish.Selector{Text: "Alpha"}, []byte("manual\n"))
	if err != nil {
		t.Fatal(err)
	}
	out2, _, err := p.Publish([]byte(docSource), publish.Selector{Text: "Alpha"}, out1)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out1, out2) {
		t.Fatalf("republishing unchanged source changed target:\n%s\n----\n%s", out1, out2)
	}
	out3, _, err := p.Publish([]byte(docSource), publish.Selector{Text: "Alpha"}, out2)
	if err != nil || !bytes.Equal(out1, out3) {
		t.Fatalf("third publish not stable: err=%v", err)
	}
	if strings.TrimSpace(rec1.GeneratedHTML) != strings.TrimSpace(regionBody(out3, "alpha")) {
		t.Fatal("generated content drift across identical publishes")
	}
}

func TestInvalidTargetsRejected(t *testing.T) {
	p := publish.New(newMarkdown())
	src := []byte(docSource)
	good := "<!-- goldmark:chapter:begin:alpha -->\nZZUNIQUECONTENTZZ\n<!-- goldmark:chapter:end:alpha -->\n"
	valid, _, err := p.Publish(src, publish.Selector{Text: "Alpha"}, []byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(valid), "ZZUNIQUECONTENTZZ") {
		t.Fatal("old region content should have been replaced")
	}

	cases := map[string][]byte{
		"begin without end":  []byte("<!-- goldmark:chapter:begin:alpha -->\nx\n"),
		"end without begin":  []byte("<!-- goldmark:chapter:end:alpha -->\n"),
		"duplicated begin":   []byte("<!-- goldmark:chapter:begin:alpha -->\na\n<!-- goldmark:chapter:begin:alpha -->\nb\n<!-- goldmark:chapter:end:alpha -->\n"),
		"end before begin":   []byte("<!-- goldmark:chapter:end:alpha -->\nx\n<!-- goldmark:chapter:begin:alpha -->\n"),
		"marker mid line":    []byte("xx<!-- goldmark:chapter:begin:alpha -->\n<!-- goldmark:chapter:end:alpha -->\n"),
		"text on end marker": []byte("<!-- goldmark:chapter:begin:alpha -->\nx\n<!-- goldmark:chapter:end:alpha -->tail\n"),
	}
	for name, target := range cases {
		t.Run(name, func(t *testing.T) {
			original := append([]byte(nil), target...)
			out, _, err := p.Publish(src, publish.Selector{Text: "Alpha"}, target)
			if !errors.Is(err, publish.ErrInvalidTarget) {
				t.Fatalf("want ErrInvalidTarget, got %v (out=%s)", err, out)
			}
			if !bytes.Equal(original, target) {
				t.Fatal("target slice must not be modified on failure")
			}
		})
	}
}

func TestCustomKey(t *testing.T) {
	p := publish.New(newMarkdown())
	out, rec, err := p.Publish([]byte("# Weird? Heading!\n\nx\n"),
		publish.Selector{Text: "Weird? Heading!", Key: "custom.key-1"}, []byte("m\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "begin:custom.key-1") || rec.Key != "custom.key-1" {
		t.Fatalf("custom key not used:\n%s", out)
	}
	if _, _, err := p.Publish([]byte("# x\n\ny\n"), publish.Selector{Text: "x", Key: "bad key"}, nil); err == nil {
		t.Fatal("invalid key must be rejected")
	}
}

func TestRecordPersistsSourceVersionChapterAndResult(t *testing.T) {
	store := publish.NewMemoryStore()
	p := publish.New(newMarkdown(), publish.WithStore(store))
	if _, _, err := p.Publish([]byte(docSource), publish.Selector{Text: "Alpha"}, nil); err != nil {
		t.Fatal(err)
	}
	rec := mustRecord(t, p, "alpha")
	if rec.ChapterText != "Alpha" || rec.ChapterID != "alpha" {
		t.Fatalf("record target chapter wrong: %+v", rec)
	}
	if !strings.Contains(rec.GeneratedHTML, "deepest content") {
		t.Fatal("record generated result missing nested subsection")
	}
	if got := strings.Join(rec.Footnotes, ","); got != "a,b,c" {
		t.Fatalf("record footnotes = %q", got)
	}
	if _, err := store.Load("missing"); !errors.Is(err, publish.ErrRecordNotFound) {
		t.Fatalf("want ErrRecordNotFound, got %v", err)
	}
}

type failingStore struct{ fail bool }

func (s *failingStore) Load(string) (*publish.Record, error) {
	return nil, publish.ErrRecordNotFound
}
func (s *failingStore) Save(*publish.Record) error {
	if s.fail {
		return errors.New("disk full")
	}
	return nil
}

func TestPublishFileAtomicAndStoreFailureLeavesTargetUntouched(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target.html")
	p := publish.New(newMarkdown())

	rec, err := p.PublishFile([]byte(docSource), publish.Selector{Text: "Alpha"}, path)
	if err != nil {
		t.Fatal(err)
	}
	if rec == nil {
		t.Fatal("missing record")
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(first), "manual") && len(first) == 0 {
		t.Fatal("initial file empty")
	}

	// Add manual content, then republish a changed source.
	withManual := append(first, []byte("<!-- MANUAL FOOTER -->\n")...)
	if err := os.WriteFile(path, withManual, 0o644); err != nil {
		t.Fatal(err)
	}
	changed := []byte(strings.ReplaceAll(docSource, "nested section content", "NESTED CHANGED CONTENT"))
	if _, err := p.PublishFile(changed, publish.Selector{Text: "Alpha"}, path); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if !strings.Contains(string(second), "MANUAL FOOTER") || !strings.Contains(string(second), "NESTED CHANGED CONTENT") {
		t.Fatalf("manual/footer or update lost:\n%s", second)
	}
	if strings.Contains(string(second), "nested section content") {
		t.Fatal("old generated content still present")
	}

	// A failing store must prevent the target file from being replaced.
	bad := publish.New(newMarkdown(), publish.WithStore(&failingStore{fail: true}))
	_, err = bad.PublishFile(changed, publish.Selector{Text: "Alpha"}, path)
	if err == nil {
		t.Fatal("want error from failing store")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(second, after) {
		t.Fatal("target modified despite commit failure")
	}
}

func TestFileStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := publish.NewFileStore(filepath.Join(dir, "records.json"))
	p := publish.New(newMarkdown(), publish.WithStore(store))

	if _, _, err := p.Publish([]byte(docSource), publish.Selector{Text: "Alpha"}, nil); err != nil {
		t.Fatal(err)
	}
	reloaded := publish.New(newMarkdown(), publish.WithStore(publish.NewFileStore(filepath.Join(dir, "records.json"))))
	rec := mustRecord(t, reloaded, "alpha")
	if rec.ChapterText != "Alpha" || !strings.Contains(rec.GeneratedHTML, "Deep One") {
		t.Fatalf("record did not survive reload: %+v", rec)
	}
	if _, err := os.Stat(filepath.Join(dir, "records.json")); err != nil {
		t.Fatal(err)
	}
}

func TestNormalConversionUnaffectedWithoutPublishing(t *testing.T) {
	// Constructing a Publisher must not reconfigure the shared markdown
	// instance: ordinary Convert output stays identical with and without a
	// publisher present.
	md := newMarkdown()
	var without bytes.Buffer
	if err := md.Convert([]byte(docSource), &without); err != nil {
		t.Fatal(err)
	}
	_ = publish.New(md)
	var with bytes.Buffer
	if err := md.Convert([]byte(docSource), &with); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(without.Bytes(), with.Bytes()) {
		t.Fatal("Constructing a Publisher changed ordinary Convert behavior")
	}
	if !strings.Contains(with.String(), "intro paragraph") {
		t.Fatal("ordinary conversion output looks broken")
	}
}

func TestConcurrentPublish(t *testing.T) {
	p := publish.New(newMarkdown())
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := p.Publish([]byte(docSource), publish.Selector{Text: "Alpha"}, nil)
			if err != nil {
				t.Errorf("concurrent publish: %v", err)
			}
		}()
	}
	wg.Wait()
}

func TestPlainMarkdownWithoutFootnoteExtension(t *testing.T) {
	p := publish.New(goldmark.New())
	src := []byte("# Alpha\n\nplain chapter with **bold**.\n\n# Beta\n\nx\n")
	ch := mustExtract(t, p, []byte(src), publish.Selector{Text: "Alpha"})
	html := string(mustRender(t, p, ch))
	if !strings.Contains(html, "<strong>bold</strong>") {
		t.Fatalf("plain render wrong:\n%s", html)
	}
	// Literal [^x] is still an incomplete reference even without the
	// extension, because such a chapter could never render a working note.
	if _, err := p.Extract([]byte("# A\n\nsee [^x]\n"), publish.Selector{Text: "A"}); !errors.Is(err, publish.ErrIncompleteFootnotes) {
		t.Fatalf("want ErrIncompleteFootnotes, got %v", err)
	}
}

func mustRender(t *testing.T, p *publish.Publisher, ch *publish.Chapter) []byte {
	t.Helper()
	out, err := p.Render(ch)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	return out
}

func mustRecord(t *testing.T, p *publish.Publisher, key string) *publish.Record {
	t.Helper()
	rec, err := p.Store().Load(key)
	if err != nil {
		t.Fatalf("Load(%q): %v", key, err)
	}
	return rec
}

// regionBody extracts the inner content of a chapter region.
func regionBody(target []byte, key string) string {
	s := string(target)
	begin := "<!-- goldmark:chapter:begin:" + key + " -->\n"
	end := "\n<!-- goldmark:chapter:end:" + key + " -->"
	i := strings.Index(s, begin)
	if i < 0 {
		return ""
	}
	rest := s[i+len(begin):]
	j := strings.Index(rest, end)
	if j < 0 {
		return ""
	}
	return rest[:j]
}
