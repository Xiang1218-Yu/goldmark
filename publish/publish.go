// Package publish provides AST based chapter publishing on top of an
// existing goldmark.Markdown instance.
//
// A chapter is a markdown section headed by an ATX or setext heading.
// Chapters are selected by heading id or by the exact heading text and are
// rendered independently: the extracted chapter is turned into a
// self-contained markdown fragment that carries the heading, all nested
// subsections and every footnote / link reference definition it actually
// uses. The fragment is parsed and rendered through the very same
// [goldmark.Markdown] parser and renderer chain that powers ordinary
// conversions, so chapter output is always produced by goldmark itself.
//
// Publishing never edits a document in place. A rendered chapter occupies a
// marker delimited region inside a target document; on re-publish only the
// generated region is replaced, content outside of the region (manual
// content and regions owned by other chapters) is preserved byte for byte.
// All validation, parsing, rendering and merging happens in memory before a
// [Record] or the target file is touched, so failures can never leave a half
// published result.
//
// This package does not register parsers, renderers or extensions on the
// supplied markdown instance and does not alter goldmark's default behavior;
// documents that are never passed to a [Publisher] are converted exactly as
// before.
package publish

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Errors returned by the package. They are wrapped with contextual
// information, so callers should use errors.Is to test for them.
var (
	// ErrSelectorEmpty is returned when a Selector specifies neither an id
	// nor a heading text, or specifies both.
	ErrSelectorEmpty = errors.New("publish: selector must specify exactly one of heading id or heading text")

	// ErrHeadingNotFound is returned when no heading matches the selector.
	ErrHeadingNotFound = errors.New("publish: heading not found")

	// ErrAmbiguousHeading is returned when more than one heading matches
	// the selector.
	ErrAmbiguousHeading = errors.New("publish: heading selection is not unique")

	// ErrIncompleteFootnotes is returned when a chapter references a
	// footnote ([^label]) for which no definition exists in the source
	// document.
	ErrIncompleteFootnotes = errors.New("publish: incomplete footnote references")

	// ErrInvalidTarget is returned when the publish target contains
	// unmatched, duplicated or otherwise malformed chapter markers.
	ErrInvalidTarget = errors.New("publish: invalid chapter markers in publish target")

	// ErrRecordNotFound is returned by Store implementations when no
	// record exists for a chapter key.
	ErrRecordNotFound = errors.New("publish: record not found")
)

// Selector selects a chapter heading in a source document.
//
// Exactly one of ID and Text must be set. ID matches the heading id produced
// by goldmark (parser.WithAutoHeadingID) or an explicit {#id} attribute
// (parser.WithHeadingAttribute). Text matches the exact plain text of the
// heading. Key optionally overrides the stable key used for the publish
// region and the record; it defaults to the heading id or to a slug derived
// from the heading text.
type Selector struct {
	ID   string
	Text string
	Key  string
}

func (s Selector) validate() error {
	if (s.ID == "") == (s.Text == "") {
		return ErrSelectorEmpty
	}
	return nil
}

func (s Selector) describe() string {
	if s.ID != "" {
		return "id " + s.ID
	}
	return "text " + s.Text
}

// Chapter is a chapter extracted from a source document.
//
// Markdown is a self-contained markdown fragment: the selected heading, all
// nested subsections and every externally defined footnote / link reference
// the chapter uses. AST is the document parsed from Markdown through the
// configured goldmark parser; it is unrelated to the source document tree and
// can be walked and rendered independently.
type Chapter struct {
	// Selector is the selector the chapter was selected with.
	Selector Selector

	// Key is the stable key used for the publish region and record.
	Key string

	// Level is the heading level (1-6).
	Level int

	// ID is the heading id, if the parser assigned one.
	ID string

	// Text is the plain heading text.
	Text string

	// Markdown is the self-contained chapter fragment.
	Markdown []byte

	// AST is the independent document tree parsed from Markdown.
	AST *ast.Document

	// Heading is the selected heading node inside AST.
	Heading *ast.Heading

	// Footnotes lists the footnote labels referenced by the chapter, in
	// first-use order.
	Footnotes []string

	// ReferenceDefinitions lists the labels of externally defined link
	// references attached to the fragment, in source order.
	ReferenceDefinitions []string
}

// Render renders the chapter independently through the configured goldmark
// renderer. It never touches the source document tree.
func (p *Publisher) Render(c *Chapter) ([]byte, error) {
	var buf bytes.Buffer
	if err := p.md.Renderer().Render(&buf, c.Markdown, c.AST); err != nil {
		return nil, fmt.Errorf("publish: render chapter (%s): %w", c.Selector.describe(), err)
	}
	return buf.Bytes(), nil
}

// Record is the persisted state of a published chapter.
type Record struct {
	// Key is the stable chapter key the record is stored under.
	Key string `json:"key"`

	// SourceVersion is the hex encoded SHA-256 digest of the source
	// document the chapter was generated from.
	SourceVersion string `json:"source_version"`

	// ChapterID is the selected heading id, if any.
	ChapterID string `json:"chapter_id,omitempty"`

	// ChapterText is the selected heading text.
	ChapterText string `json:"chapter_text"`

	// HeadingLevel is the selected heading level.
	HeadingLevel int `json:"heading_level"`

	// Footnotes are the footnote labels referenced by the chapter.
	Footnotes []string `json:"footnotes,omitempty"`

	// References are the external link reference labels attached to the
	// fragment.
	References []string `json:"references,omitempty"`

	// GeneratedHTML is the last generated region content.
	GeneratedHTML string `json:"generated_html"`

	// PublishedAt is the generation time (UTC).
	PublishedAt time.Time `json:"published_at"`
}

// Option configures a Publisher.
type Option func(*publisherConfig)

type publisherConfig struct {
	store Store
}

// WithStore configures the record store used by the publisher. Without this
// option a new in-memory store is used.
func WithStore(store Store) Option {
	return func(c *publisherConfig) { c.store = store }
}

// Publisher publishes chapters extracted from markdown documents.
//
// A Publisher is safe for concurrent use. Extract and Render may run in
// parallel; Publish serializes record commits.
type Publisher struct {
	md    goldmark.Markdown
	store Store
	mu    sync.Mutex
}

// New returns a Publisher that uses the given goldmark instance for parsing
// and rendering. The instance is never reconfigured.
func New(md goldmark.Markdown, opts ...Option) *Publisher {
	cfg := &publisherConfig{store: NewMemoryStore()}
	for _, opt := range opts {
		opt(cfg)
	}
	if cfg.store == nil {
		cfg.store = NewMemoryStore()
	}
	return &Publisher{md: md, store: cfg.store}
}

// Store returns the record store backing the publisher.
func (p *Publisher) Store() Store { return p.store }

// Markdown returns the backing goldmark instance.
func (p *Publisher) Markdown() goldmark.Markdown { return p.md }

// Extract parses source through the configured goldmark parser, selects the
// chapter matched by sel and builds its self-contained fragment.
//
// It fails with ErrHeadingNotFound, ErrAmbiguousHeading or
// ErrIncompleteFootnotes without mutating any state.
func (p *Publisher) Extract(source []byte, sel Selector, popts ...parser.ParseOption) (*Chapter, error) {
	if err := sel.validate(); err != nil {
		return nil, err
	}
	doc := p.md.Parser().Parse(text.NewReader(source), popts...)
	ch, err := p.extract(doc, source, sel, popts)
	if err != nil {
		return nil, err
	}
	return ch, nil
}

// Publish extracts and renders the chapter, then merges its generated region
// into target. The merged target and the committed record are returned.
//
// Parsing, validation, rendering and merging complete before the record is
// saved, so any error leaves target and the store untouched.
func (p *Publisher) Publish(source []byte, sel Selector, target []byte, popts ...parser.ParseOption) ([]byte, *Record, error) {
	chapter, err := p.Extract(source, sel, popts...)
	if err != nil {
		return nil, nil, err
	}
	generated, err := p.Render(chapter)
	if err != nil {
		return nil, nil, err
	}
	body := bytes.TrimRight(generated, "\r\n")
	merged, err := mergeRegion(target, body, chapter.Key)
	if err != nil {
		return nil, nil, err
	}
	record := &Record{
		Key:           chapter.Key,
		SourceVersion: sourceVersion(source),
		ChapterID:     chapter.ID,
		ChapterText:   chapter.Text,
		HeadingLevel:  chapter.Level,
		Footnotes:     chapter.Footnotes,
		References:    chapter.ReferenceDefinitions,
		GeneratedHTML: string(body),
		PublishedAt:   time.Now().UTC(),
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.store.Save(record); err != nil {
		return nil, nil, fmt.Errorf("publish: save record for %s: %w", sel.describe(), err)
	}
	return merged, record, nil
}

// PublishFile publishes into the file at targetPath (a missing file is
// treated as empty) and commits the result atomically: a sibling temporary
// file is written and renamed over targetPath, so an interrupted publish can
// never leave a truncated target. The record is saved before the rename.
func (p *Publisher) PublishFile(source []byte, sel Selector, targetPath string, popts ...parser.ParseOption) (*Record, error) {
	target, err := os.ReadFile(targetPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("publish: read target %q: %w", targetPath, err)
	}
	merged, record, err := p.Publish(source, sel, target, popts...)
	if err != nil {
		return nil, err
	}

	dir := filepath.Dir(targetPath)
	mode := os.FileMode(0o644)
	if info, statErr := os.Stat(targetPath); statErr == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(dir, ".goldmark-publish-*")
	if err != nil {
		return nil, fmt.Errorf("publish: create temporary target: %w", err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(merged); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("publish: write temporary target: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("publish: close temporary target: %w", err)
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return nil, fmt.Errorf("publish: set temporary target permissions: %w", err)
	}
	if err := os.Rename(tmpName, targetPath); err != nil {
		return nil, fmt.Errorf("publish: commit target %q: %w", targetPath, err)
	}
	committed = true
	return record, nil
}

// sourceVersion returns the hex SHA-256 digest of a source document.
func sourceVersion(source []byte) string {
	sum := sha256.Sum256(source)
	return hex.EncodeToString(sum[:])
}

// keyPattern restricts region keys so marker lines stay well formed.
var keyPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?$`)

// resolveKey derives the stable region key for a selector / heading pair.
func resolveKey(sel Selector, headingText []byte) (string, error) {
	if sel.Key != "" {
		if !keyPattern.MatchString(sel.Key) {
			return "", fmt.Errorf("publish: invalid region key %q", sel.Key)
		}
		return sel.Key, nil
	}
	if sel.ID != "" {
		if !keyPattern.MatchString(sel.ID) {
			return "", fmt.Errorf("publish: heading id %q cannot be used as a region key; set Selector.Key", sel.ID)
		}
		return sel.ID, nil
	}
	key := slug(headingText)
	if key == "" {
		key = "section"
	}
	return key, nil
}

// slug mirrors the id generation implemented by parser.ids.Generate for ASCII
// content: alphanumerics are lowercased, spaces, '-' and '_' become '-', other
// bytes are dropped.
func slug(value []byte) string {
	var b []byte
	for i := 0; i < len(value); {
		v := value[i]
		l := util.UTF8Len(v)
		i += int(l)
		if l != 1 {
			continue
		}
		if util.IsAlphaNumeric(v) {
			if 'A' <= v && v <= 'Z' {
				v += 'a' - 'A'
			}
			b = append(b, v)
		} else if util.IsSpace(v) || v == '-' || v == '_' {
			b = append(b, '-')
		}
	}
	return string(b)
}
