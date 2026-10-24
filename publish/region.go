package publish

import (
	"bytes"
	"fmt"
)

// Marker names delimiting generated regions inside publish targets.
const (
	markerScheme = "goldmark:chapter"
)

func beginMarker(key string) []byte {
	return []byte("<!-- " + markerScheme + ":begin:" + key + " -->")
}

func endMarker(key string) []byte {
	return []byte("<!-- " + markerScheme + ":end:" + key + " -->")
}

// mergeRegion merges generated chapter content into a target document.
//
// A region is delimited by a begin line and an end line carrying the chapter
// key. When the region already exists only the bytes between the marker lines
// are replaced; everything before the begin line and after the end line
// (manual content and regions belonging to other chapters) is preserved. When
// the region does not exist it is appended to the target.
//
// Unmatched, duplicated or non-line-anchored markers make the target invalid
// and are reported without producing output.
func mergeRegion(target, body []byte, key string) ([]byte, error) {
	begin := beginMarker(key)
	end := endMarker(key)

	beginIdxs := indexAll(target, begin)
	endIdxs := indexAll(target, end)
	if len(beginIdxs) != len(endIdxs) || len(beginIdxs) > 1 {
		return nil, fmt.Errorf("%w: unmatched or duplicated markers for key %q", ErrInvalidTarget, key)
	}

	if len(beginIdxs) == 0 {
		return appendRegion(target, begin, body, end), nil
	}

	b, e := beginIdxs[0], endIdxs[0]
	if b >= e {
		return nil, fmt.Errorf("%w: end marker precedes begin marker for key %q", ErrInvalidTarget, key)
	}
	// Markers must occupy their own lines; inline occurrences are treated as
	// corrupted manual edits.
	if b > 0 && target[b-1] != '\n' {
		return nil, fmt.Errorf("%w: begin marker for key %q is not at a line start", ErrInvalidTarget, key)
	}
	contentStart := lineAfter(target, b+len(begin))
	if contentStart < 0 {
		return nil, fmt.Errorf("%w: dangling begin marker for key %q", ErrInvalidTarget, key)
	}
	if e > 0 && target[e-1] != '\n' {
		return nil, fmt.Errorf("%w: end marker for key %q is not at a line start", ErrInvalidTarget, key)
	}
	after := e + len(end)
	if after < len(target) && target[after] != '\n' && target[after] != '\r' {
		return nil, fmt.Errorf("%w: trailing text on the end marker line for key %q", ErrInvalidTarget, key)
	}

	merged := make([]byte, 0, len(target)+len(body))
	merged = append(merged, target[:contentStart]...)
	merged = append(merged, body...)
	merged = append(merged, '\n')
	merged = append(merged, target[e:]...)
	return merged, nil
}

// appendRegion builds a target with a new region appended at its end.
func appendRegion(target, begin, body, end []byte) []byte {
	out := append([]byte{}, target...)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	out = append(out, begin...)
	out = append(out, '\n')
	out = append(out, body...)
	if len(body) == 0 || body[len(body)-1] != '\n' {
		out = append(out, '\n')
	}
	out = append(out, end...)
	out = append(out, '\n')
	return out
}

// lineAfter returns the offset immediately following the newline terminating
// the marker line at idx, or -1 if the marker is not followed by a newline.
func lineAfter(target []byte, idx int) int {
	if idx < len(target) && target[idx] == '\r' {
		idx++
	}
	if idx >= len(target) || target[idx] != '\n' {
		return -1
	}
	return idx + 1
}

// indexAll returns every start offset of sep in b.
func indexAll(b, sep []byte) []int {
	var idxs []int
	for off := 0; ; {
		i := bytes.Index(b[off:], sep)
		if i < 0 {
			return idxs
		}
		idxs = append(idxs, off+i)
		off += i + len(sep)
	}
}
