package publish

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Store persists publish records keyed by the stable chapter key.
type Store interface {
	// Load returns the record for key. It returns an error wrapping
	// ErrRecordNotFound when no record exists.
	Load(key string) (*Record, error)

	// Save stores rec, committing it durably.
	Save(rec *Record) error
}

// MemoryStore keeps records in memory. It is safe for concurrent use.
type MemoryStore struct {
	mu      sync.Mutex
	records map[string]*Record
}

// NewMemoryStore returns an empty in-memory record store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{records: map[string]*Record{}}
}

// Load implements Store.
func (s *MemoryStore) Load(key string) (*Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[key]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrRecordNotFound, key)
	}
	cp := *rec
	return &cp, nil
}

// Save implements Store.
func (s *MemoryStore) Save(rec *Record) error {
	if rec == nil {
		return errors.New("publish: cannot save a nil record")
	}
	cp := *rec
	s.mu.Lock()
	s.records[rec.Key] = &cp
	s.mu.Unlock()
	return nil
}

// FileStore keeps records as a single JSON document on disk. Writes are
// atomic: the document is written to a sibling temporary file and renamed
// into place, so a crashed Save can never leave a truncated record file. It
// is safe for concurrent use.
type FileStore struct {
	mu   sync.Mutex
	path string
	mode os.FileMode
}

// NewFileStore returns a file backed store at path. The file is created on
// the first Save; it must not be shared with unrelated content.
func NewFileStore(path string) *FileStore {
	return &FileStore{path: path, mode: 0o644}
}

// Load implements Store.
func (s *FileStore) Load(key string) (*Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.readAll()
	if err != nil {
		return nil, err
	}
	rec, ok := records[key]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrRecordNotFound, key)
	}
	return rec, nil
}

// Save implements Store.
func (s *FileStore) Save(rec *Record) error {
	if rec == nil {
		return errors.New("publish: cannot save a nil record")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.readAll()
	if err != nil {
		return err
	}
	cp := *rec
	records[rec.Key] = &cp
	return s.writeAll(records)
}

func (s *FileStore) readAll() (map[string]*Record, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]*Record{}, nil
		}
		return nil, fmt.Errorf("publish: read record store %q: %w", s.path, err)
	}
	records := map[string]*Record{}
	if len(bytes.TrimSpace(data)) == 0 {
		return records, nil
	}
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("publish: decode record store %q: %w", s.path, err)
	}
	return records, nil
}

func (s *FileStore) writeAll(records map[string]*Record) error {
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return fmt.Errorf("publish: encode record store: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".goldmark-records-*")
	if err != nil {
		return fmt.Errorf("publish: create temporary record store: %w", err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("publish: write temporary record store: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("publish: close temporary record store: %w", err)
	}
	if err := os.Chmod(tmpName, s.mode); err != nil {
		return fmt.Errorf("publish: chmod temporary record store: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("publish: commit record store %q: %w", s.path, err)
	}
	committed = true
	return nil
}
