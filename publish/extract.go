package publish

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/yuin/goldmark/ast"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// extract performs the AST guided extraction. All parsing is done by the
// configured goldmark parser; this function only inspects and slices the
// parsed tree and its source.
func (p *Publisher) extract(doc ast.Node, source []byte, sel Selector, popts []parser.ParseOption) (*Chapter, error) {
	var (
		children []ast.Node
		lrds     []*ast.LinkReferenceDefinition
		fnList   *east.FootnoteList
	)
	for c := doc.FirstChild(); c != nil; c = c.NextSibling() {
		children = append(children, c)
		switch n := c.(type) {
		case *ast.LinkReferenceDefinition:
			lrds = append(lrds, n)
		case *east.FootnoteList:
			fnList = n
		}
	}

	// Resolve the heading selection against the top level outline.
	var matches []int
	for i, c := range children {
		h, ok := c.(*ast.Heading)
		if !ok {
			continue
		}
		if selectorMatchesHeading(sel, h, source) {
			matches = append(matches, i)
		}
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("%w: no heading with %s", ErrHeadingNotFound, sel.describe())
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("%w: %d headings match %s", ErrAmbiguousHeading, len(matches), sel.describe())
	}
	headIdx := matches[0]
	heading := children[headIdx].(*ast.Heading)
	headingText := heading.Text(source)
	key, err := resolveKey(sel, headingText)
	if err != nil {
		return nil, err
	}

	// A chapter spans from its heading line up to the next top level heading
	// of the same or a higher level. Deeper headings are nested subsections
	// and are naturally contained in the span.
	secStart := lineStart(source, heading.Pos())
	boundary := len(children)
	for i := headIdx + 1; i < len(children); i++ {
		if h, ok := children[i].(*ast.Heading); ok && h.Level <= heading.Level {
			boundary = i
			break
		}
	}
	var secStop int
	if boundary < len(children) {
		secStop = lineStart(source, children[boundary].Pos())
	} else {
		secStop = len(source)
	}
	sectionNodes := children[headIdx:boundary]
	sectionRaw := source[secStart:secStop]

	// Footnote state: map global footnote indexes (assigned during the full
	// document parse) back to their labels, collect the references used by
	// the chapter and reject references that never resolved to a definition.
	indexToRef := map[int]string{}
	refToFootnote := map[string]*east.Footnote{}
	if fnList != nil {
		for fn := fnList.FirstChild(); fn != nil; fn = fn.NextSibling() {
			f := fn.(*east.Footnote)
			if f.Index >= 0 {
				indexToRef[f.Index] = string(f.Ref)
				refToFootnote[string(f.Ref)] = f
			}
		}
	}
	// Collect footnote references used by the chapter body, then expand the
	// set across references found inside footnote definitions (a footnote may
	// reference another footnote). References that never resolved to a
	// definition are rejected.
	fnScan := newFootnoteScan(source)
	fnScan.walk(sectionNodes)
	visitedFns := map[string]bool{}
	for {
		grew := false
		for _, idx := range append([]int(nil), fnScan.usedOrder...) {
			label := indexToRef[idx]
			if label == "" || visitedFns[label] {
				continue
			}
			visitedFns[label] = true
			if fn := refToFootnote[label]; fn != nil {
				fnScan.walk([]ast.Node{fn})
				grew = true
			}
		}
		if !grew {
			break
		}
	}
	usedRefs := fnScan.usedLabels(indexToRef)
	if unresolved := fnScan.unresolvedLabels(); len(unresolved) > 0 {
		sort.Strings(unresolved)
		return nil, fmt.Errorf("%w: [%s] in %s", ErrIncompleteFootnotes, strings.Join(unresolved, ", "), sel.describe())
	}

	insideSection := func(pos int) bool { return pos >= secStart && pos < secStop }

	var externalFootnotes []*east.Footnote
	for _, ref := range usedRefs {
		fn := refToFootnote[ref]
		if fn == nil || insideSection(fn.Pos()) {
			continue // definition is already part of the raw section slice
		}
		externalFootnotes = append(externalFootnotes, fn)
	}
	sort.SliceStable(externalFootnotes, func(i, j int) bool {
		return externalFootnotes[i].Pos() < externalFootnotes[j].Pos()
	})

	// Link reference definitions already resolve into Link/Image nodes
	// during the full document parse, so the labels the chapter uses are
	// discovered by parsing the raw section with a context that records every
	// reference lookup goldmark performs.
	tracker := &trackingContext{Context: parser.NewContext(), lookedUp: map[string]struct{}{}}
	trackOpts := append(append([]parser.ParseOption{}, popts...), parser.WithContext(tracker))
	trimmed := bytes.TrimRight(sectionRaw, " \t\r\n")
	_ = p.md.Parser().Parse(text.NewReader(trimmed), trackOpts...)

	globalRefs := map[string]*ast.LinkReferenceDefinition{}
	for _, r := range lrds {
		key := util.ToLinkReference(r.Label)
		if _, ok := globalRefs[key]; !ok { // goldmark keeps the first definition
			globalRefs[key] = r
		}
	}
	var externalRefs []*ast.LinkReferenceDefinition
	for label := range tracker.lookedUp {
		if r, ok := globalRefs[label]; ok && !insideSection(r.Pos()) {
			externalRefs = append(externalRefs, r)
		}
	}
	sort.SliceStable(externalRefs, func(i, j int) bool {
		return externalRefs[i].Pos() < externalRefs[j].Pos()
	})

	// Build the self-contained fragment: the verbatim section followed by
	// the externally located definitions it uses.
	fragment := append([]byte{}, trimmed...)
	refLabels := make([]string, 0, len(externalRefs))
	for _, r := range externalRefs {
		fragment = append(fragment, '\n', '\n')
		fragment = append(fragment, referenceDefinitionRaw(source, r)...)
		refLabels = append(refLabels, string(r.Label))
	}
	for _, f := range externalFootnotes {
		fragment = append(fragment, '\n', '\n')
		fragment = append(fragment, footnoteRaw(source, f)...)
	}
	fragment = append(fragment, '\n')

	// The fragment is an independent document parsed through the same
	// goldmark parser (and thus the same extensions) as full documents.
	fragDoc := p.md.Parser().Parse(text.NewReader(fragment), popts...)
	// Safety net: the independently parsed fragment must not contain a
	// reference without a definition.
	fragScan := newFootnoteScan(fragment)
	fragScan.walk([]ast.Node{fragDoc})
	if unresolved := fragScan.unresolvedLabels(); len(unresolved) > 0 {
		sort.Strings(unresolved)
		return nil, fmt.Errorf("%w: [%s] after extraction", ErrIncompleteFootnotes, strings.Join(unresolved, ", "))
	}
	var fragHeading *ast.Heading
	for c := fragDoc.FirstChild(); c != nil && fragHeading == nil; c = c.NextSibling() {
		if h, ok := c.(*ast.Heading); ok {
			fragHeading = h
		}
	}

	id, _ := headingID(heading)

	return &Chapter{
		Selector:             sel,
		Key:                  key,
		Level:                heading.Level,
		ID:                   id,
		Text:                 string(headingText),
		Markdown:             fragment,
		AST:                  fragDoc.(*ast.Document),
		Heading:              fragHeading,
		Footnotes:            usedRefs,
		ReferenceDefinitions: refLabels,
	}, nil
}

// selectorMatchesHeading reports whether a heading satisfies the selector.
// Exactly one of sel.ID / sel.Text is set (validated before this call).
func selectorMatchesHeading(sel Selector, h *ast.Heading, source []byte) bool {
	if sel.ID != "" {
		id, ok := headingID(h)
		return ok && id == sel.ID
	}
	return string(h.Text(source)) == sel.Text
}

// headingID returns the id attribute goldmark assigned to a heading.
func headingID(h *ast.Heading) (string, bool) {
	v, ok := h.AttributeString("id")
	if !ok || v == nil {
		return "", false
	}
	switch id := v.(type) {
	case []byte:
		return string(id), true
	case string:
		return id, true
	default:
		return fmt.Sprintf("%v", id), true
	}
}

// trackingContext wraps a parser.Context and records every link reference
// label lookup performed while parsing. It delegates all other behavior to
// the wrapped context.
type trackingContext struct {
	parser.Context
	lookedUp map[string]struct{}
}

func (t *trackingContext) Reference(label string) (parser.Reference, bool) {
	t.lookedUp[label] = struct{}{}
	return t.Context.Reference(label)
}

// footnoteScan walks AST nodes and collects referenced footnote indexes and
// the labels of references that did not resolve against any definition (left
// as literal [^label] text by goldmark). The same scanner can be invoked
// repeatedly (for example over footnote definition bodies) to build a
// transitive dependency set.
type footnoteScan struct {
	source []byte

	used       map[int]bool
	usedOrder  []int
	unres      map[string]bool
	unresOrder []string
}

func newFootnoteScan(source []byte) *footnoteScan {
	return &footnoteScan{
		source: source,
		used:   map[int]bool{},
		unres:  map[string]bool{},
	}
}

func (s *footnoteScan) walk(roots []ast.Node) {
	scanner := newFootnoteRefScanner(s.source, func(label string) {
		if !s.unres[label] {
			s.unres[label] = true
			s.unresOrder = append(s.unresOrder, label)
		}
	})

	walker := func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch node := n.(type) {
		case *ast.CodeSpan:
			// '[^x]' inside inline code is not a footnote reference.
			scanner.reset()
			return ast.WalkSkipChildren, nil
		case *ast.FencedCodeBlock, *ast.CodeBlock, *ast.HTMLBlock:
			scanner.reset()
			return ast.WalkSkipChildren, nil
		case *east.FootnoteLink:
			// A resolved footnote reference; reset so its text cannot join a
			// literal run.
			scanner.reset()
			if !s.used[node.Index] {
				s.used[node.Index] = true
				s.usedOrder = append(s.usedOrder, node.Index)
			}
		case *ast.Text:
			scanner.addSegment(node.Segment)
		}
		return ast.WalkContinue, nil
	}
	for _, root := range roots {
		_ = ast.Walk(root, walker)
	}
}

// usedLabels maps referenced indexes back to their labels.
func (s *footnoteScan) usedLabels(indexToRef map[int]string) []string {
	labels := make([]string, 0, len(s.usedOrder))
	seen := map[string]bool{}
	for _, idx := range s.usedOrder {
		label, ok := indexToRef[idx]
		if ok && label != "" && !seen[label] {
			seen[label] = true
			labels = append(labels, label)
		}
	}
	return labels
}

// unresolvedLabels returns labels referenced without a definition, in
// encounter order.
func (s *footnoteScan) unresolvedLabels() []string {
	return s.unresOrder
}

// footnoteRefScanner finds literal "[^label]" occurrences across runs of
// contiguous ast.Text segments. Resolved footnote references never reach the
// scanner (they are FootnoteLink nodes), so anything found here is a
// reference without a definition.
type footnoteRefScanner struct {
	source []byte
	onRef  func(label string)

	buf    []byte
	start  int // source offset of buf[0]; meaningful only when contiguous
	cursor int // buf offset up to which scanning has completed
	contig bool
}

func newFootnoteRefScanner(source []byte, onRef func(string)) *footnoteRefScanner {
	return &footnoteRefScanner{source: source, onRef: onRef}
}

func (s *footnoteRefScanner) reset() {
	s.buf = s.buf[:0]
	s.cursor = 0
	s.contig = false
}

func (s *footnoteRefScanner) addSegment(seg text.Segment) {
	value := s.source[seg.Start:seg.Stop]
	if s.contig && s.start+len(s.buf) == seg.Start {
		s.buf = append(s.buf, value...)
	} else {
		s.buf = append(s.buf[:0], value...)
		s.start = seg.Start
		s.cursor = 0
	}
	s.contig = true
	s.scan()
}

func (s *footnoteRefScanner) scan() {
	b := s.buf
	i := s.cursor
	if i > 0 {
		i-- // allow a '[' straddling the previous boundary
	}
	for i < len(b) {
		j := bytes.IndexByte(b[i:], '[')
		if j < 0 {
			s.cursor = len(b)
			return
		}
		i += j
		if i+1 >= len(b) || b[i+1] != '^' {
			i++
			continue
		}
		if isEscaped(b, i) {
			i++
			continue
		}
		close := bytes.IndexByte(b[i+2:], ']')
		if close < 0 {
			// The reference may complete in a following contiguous segment.
			s.cursor = i
			return
		}
		close += i + 2
		after := close + 1
		if after < len(b) && b[after] == ':' && atLineStart(b, i) {
			// A literal footnote definition, not a reference.
			i = after
			continue
		}
		label := string(b[i+2 : close])
		s.onRef(label)
		i = close + 1
	}
	s.cursor = len(b)
}

// isEscaped reports whether the '[' at offset i is preceded by an odd number
// of backslashes.
func isEscaped(b []byte, i int) bool {
	n := 0
	for k := i - 1; k >= 0 && b[k] == '\\'; k-- {
		n++
	}
	return n%2 == 1
}

// atLineStart reports whether offset only follows a line start or horizontal
// whitespace (the shape of a block level definition).
func atLineStart(b []byte, i int) bool {
	for k := i - 1; k >= 0; k-- {
		if b[k] == '\n' {
			return true
		}
		if b[k] != ' ' && b[k] != '\t' {
			return false
		}
	}
	return true
}

// footnoteRaw reconstructs the verbatim "[^label]: ..." definition block
// (including indented continuation lines) for a footnote node.
func footnoteRaw(source []byte, fn *east.Footnote) []byte {
	start := lineStart(source, fn.Pos())
	stop := lineEnd(source, start)
	ast.Walk(fn, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if n.Type() == ast.TypeBlock {
			for i := 0; i < n.Lines().Len(); i++ {
				if s := n.Lines().At(i).Stop; s > stop {
					stop = s
				}
			}
		}
		if t, ok := n.(*ast.Text); ok {
			if t.Segment.Stop > stop {
				stop = t.Segment.Stop
			}
		}
		return ast.WalkContinue, nil
	})
	return source[start:stop]
}

// referenceDefinitionRaw reconstructs the verbatim "[label]: dest" block
// (including continuation lines carrying a title) for a definition node.
func referenceDefinitionRaw(source []byte, r *ast.LinkReferenceDefinition) []byte {
	lines := r.Lines()
	start := lineStart(source, lines.At(0).Start)
	stop := lines.At(lines.Len() - 1).Stop
	return source[start:stop]
}

func lineStart(source []byte, pos int) int {
	if pos < 0 {
		pos = 0
	}
	if pos > len(source) {
		pos = len(source)
	}
	i := pos
	for i > 0 && source[i-1] != '\n' {
		i--
	}
	return i
}

func lineEnd(source []byte, pos int) int {
	i := pos
	for i < len(source) && source[i] != '\n' {
		i++
	}
	return i
}
