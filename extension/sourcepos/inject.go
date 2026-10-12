package sourcepos

import "github.com/yuin/goldmark/util"

// scanState drives the tiny state machine of injectWriter.
type scanState int

const (
	// scanIdle means the position attribute still has to be inserted and
	// the opening tag has not been seen yet.
	scanIdle scanState = iota
	// scanLT means a '<' that may begin an opening tag was just written.
	scanLT
	// scanName means the tag name is currently being written.
	scanName
	// scanDone means the attribute was inserted (or insertion was given
	// up); the writer is now a pure pass-through.
	scanDone
)

// injectWriter wraps a util.BufWriter for the duration of a single node
// renderer call and inserts the position attribute right after the name
// of the first opening tag written by the node, for example:
//
//	<h1 data-sourcepos="3:1-3:12">
//	<img data-sourcepos="5:3-5:40" src="..." alt="...">
//
// Inserting after the tag name (instead of before the closing '>') makes
// self-closing tags (<hr/>, <img/>) and tags written through several
// Write calls work without having to understand quoted attribute values.
//
// Every wrapped node renderer emits its opening tag synchronously while
// it runs. If no opening tag is written during the call, the pending
// attribute is discarded when the injectWriter is dropped, so it can
// never leak into a sibling or descendant node.
type injectWriter struct {
	w      util.BufWriter
	marker []byte
	state  scanState
}

func newInjectWriter(w util.BufWriter, marker []byte) *injectWriter {
	iw := &injectWriter{w: w}
	if len(marker) > 0 {
		iw.marker = marker
		iw.state = scanIdle
	} else {
		iw.state = scanDone
	}
	return iw
}

func isTagNameStart(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

func isTagNameByte(b byte) bool {
	return isTagNameStart(b) ||
		b >= '0' && b <= '9' ||
		b == '-'
}

func (iw *injectWriter) finish() {
	iw.marker = nil
	iw.state = scanDone
}

// writeByte routes a single byte through the insertion state machine.
func (iw *injectWriter) writeByte(b byte) error {
	switch iw.state {
	case scanIdle:
		if err := iw.w.WriteByte(b); err != nil {
			return err
		}
		if b == '<' {
			iw.state = scanLT
		}
		return nil
	case scanLT:
		if err := iw.w.WriteByte(b); err != nil {
			return err
		}
		// Closing tags ('</...'), comments ('<!--') and declarations
		// ('<!...') cannot carry the attribute; abandon it.
		if isTagNameStart(b) {
			iw.state = scanName
		} else {
			iw.finish()
		}
		return nil
	case scanName:
		if !isTagNameByte(b) {
			if _, err := iw.w.Write(iw.marker); err != nil {
				return err
			}
			iw.finish()
		}
		return iw.w.WriteByte(b)
	default:
		return iw.w.WriteByte(b)
	}
}

// Write implements io.Writer.
func (iw *injectWriter) Write(p []byte) (int, error) {
	if iw.state == scanDone {
		return iw.w.Write(p)
	}
	for i := 0; i < len(p); i++ {
		if err := iw.writeByte(p[i]); err != nil {
			return i, err
		}
	}
	return len(p), nil
}

// WriteByte implements util.BufWriter.
func (iw *injectWriter) WriteByte(c byte) error {
	if iw.state == scanDone {
		return iw.w.WriteByte(c)
	}
	return iw.writeByte(c)
}

// WriteString implements util.BufWriter.
func (iw *injectWriter) WriteString(s string) (int, error) {
	if iw.state == scanDone {
		return iw.w.WriteString(s)
	}
	return iw.Write([]byte(s))
}

// WriteRune implements util.BufWriter.
func (iw *injectWriter) WriteRune(r rune) (int, error) {
	if iw.state == scanDone {
		return iw.w.WriteRune(r)
	}
	return iw.Write([]byte(string(r)))
}

// Available implements util.BufWriter.
func (iw *injectWriter) Available() int {
	return iw.w.Available()
}

// Buffered implements util.BufWriter.
func (iw *injectWriter) Buffered() int {
	return iw.w.Buffered()
}

// Flush implements util.BufWriter.
func (iw *injectWriter) Flush() error {
	return iw.w.Flush()
}
