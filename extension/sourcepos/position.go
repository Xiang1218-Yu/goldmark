package sourcepos

import (
	"sort"
	"strconv"

	"github.com/yuin/goldmark/ast"
)

// lineIndex maps byte offsets in a markdown source to 1-based line and
// column numbers. A column is the 1-based byte offset from the beginning
// of the line (tab characters count as one column).
type lineIndex struct {
	source []byte
	// starts[i] is the byte offset of the beginning of line i+1.
	starts []int
}

func newLineIndex(source []byte) *lineIndex {
	starts := make([]int, 1, 16)
	for i := 0; i < len(source); i++ {
		if source[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	return &lineIndex{source: source, starts: starts}
}

// location returns the 1-based line and column of the given byte offset.
func (idx *lineIndex) location(offset int) (line, col int, ok bool) {
	if offset < 0 || offset >= len(idx.source) {
		return 0, 0, false
	}
	i := sort.Search(len(idx.starts), func(i int) bool {
		return idx.starts[i] > offset
	}) - 1
	if i < 0 {
		return 0, 0, false
	}
	return i + 1, offset - idx.starts[i] + 1, true
}

// nodeSpan returns the half-open byte range [start, end) of the source
// text occupied by n and, for container nodes, their descendants.
//
// The start is taken from n.Pos when it is defined (the parser records
// the beginning of block and inline syntax, e.g. the '#' of a heading or
// the '[' of a link). The end is derived by scanning every segment held
// by the subtree (block lines, Text segments and inline RawHTML
// segments), so nodes spanning several source lines as well as nodes
// that do not keep their own lines (lists, links, emphasis, tables) are
// handled by the same code path.
//
// ok is false when the node carries no usable position information,
// which is the case for programmatically built AST nodes that are not
// associated with the source.
func nodeSpan(n ast.Node, source []byte) (start, end int, ok bool) {
	start, end = -1, -1

	// Seed the range from the parser recorded position so that leaf
	// nodes without lines or children (thematic breaks, empty headings)
	// are still located.
	if p := n.Pos(); p >= 0 && p <= len(source) {
		start, end, ok = p, p, true
	}

	consider := func(s, e int) {
		if s < 0 || e < s || s > len(source) {
			return
		}
		if e > len(source) {
			e = len(source)
		}
		if start < 0 || s < start {
			start = s
		}
		if e > end {
			end = e
		}
		ok = true
	}

	var visit func(x ast.Node)
	visit = func(x ast.Node) {
		switch t := x.(type) {
		case *ast.Text:
			consider(t.Segment.Start, t.Segment.Stop)
		case *ast.RawHTML:
			for i := 0; i < t.Segments.Len(); i++ {
				s := t.Segments.At(i)
				consider(s.Start, s.Stop)
			}
		}
		// Lines panics on inline nodes, so only block-derived nodes
		// (TypeBlock and TypeDocument) are queried.
		if x.Type() != ast.TypeInline {
			segments := x.Lines()
			for i := 0; i < segments.Len(); i++ {
				s := segments.At(i)
				consider(s.Start, s.Stop)
			}
		}
		for c := x.FirstChild(); c != nil; c = c.NextSibling() {
			visit(c)
		}
	}
	visit(n)

	if !ok {
		return -1, -1, false
	}

	// Prefer the parser recorded start over a descendant start so that
	// the range covers the node's own syntax (markers, fences, quotes).
	if p := n.Pos(); p >= 0 && p <= len(source) {
		start = p
	}
	if end < start {
		end = start
	}
	// Stored segments usually include the trailing newline. Exclude it
	// so the end points at the last content character.
	for end > start {
		c := source[end-1]
		if c != '\n' && c != '\r' {
			break
		}
		end--
	}
	return start, end, true
}

// formatAttribute renders the position attribute value for the range
// [start, end). Values follow the "line:column-line:column" convention;
// a zero-width or single position range renders the start position only.
func (c *Config) formatAttribute(idx *lineIndex, start, end int) ([]byte, bool) {
	startLine, startCol, ok := idx.location(start)
	if !ok {
		return nil, false
	}

	buf := make([]byte, 0, 32)
	buf = append(buf, ' ')
	buf = append(buf, c.AttributeName...)
	buf = append(buf, '=', '"')
	buf = strconv.AppendInt(buf, int64(startLine), 10)
	if c.Columns {
		buf = append(buf, ':')
		buf = strconv.AppendInt(buf, int64(startCol), 10)
	}

	if end > start {
		endLine, endCol, endOK := idx.location(end - 1)
		if endOK {
			renderEnd := false
			if c.Columns {
				renderEnd = endLine != startLine || endCol != startCol
			} else {
				renderEnd = endLine != startLine
			}
			if renderEnd {
				buf = append(buf, '-')
				buf = strconv.AppendInt(buf, int64(endLine), 10)
				if c.Columns {
					buf = append(buf, ':')
					buf = strconv.AppendInt(buf, int64(endCol), 10)
				}
			}
		}
	}
	buf = append(buf, '"')
	return buf, true
}
