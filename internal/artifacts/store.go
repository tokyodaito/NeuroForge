// Package artifacts implements the content-addressed artifact store (spec §9.5,
// §31).
//
// STATUS: implemented for milestone M9.
//
// Artifacts (uploaded attachments, generated images, captured screenshots,
// diff images) are stored on the filesystem keyed by SHA-256 (§9.5: "Вложения
// сохраняются content-addressed: ~/.neuroforge/artifacts/<hash>"). Large
// binaries never live as SQLite BLOBs (§31).
//
// The store is the single source of truth for artifact bytes; metadata
// (original name, MIME, size, source, project, task, confidentiality label,
// §9.5) is recorded by the caller (storage layer / image provider / visual
// engine) keyed by the hash returned here.
//
// The store is safe for concurrent use. Writes are atomic (write to temp then
// rename). The same content written twice resolves to the same path (idempotent).
package artifacts

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// ErrInvalidHash is returned when a lookup references an unknown hash.
var ErrInvalidHash = errors.New("artifacts: unknown hash")

// Label is the confidentiality label carried alongside an artifact (spec §9.5,
// §9.6). It guides redaction/external-upload policy.
type Label string

const (
	LabelPublic       Label = "public"
	LabelInternal     Label = "internal"
	LabelConfidential Label = "confidential"
)

// Store is a filesystem-backed content-addressed artifact store.
type Store struct {
	mu   sync.Mutex
	root string
}

// New opens or creates a store rooted at root (typically
// "$NEUROFORGE_HOME/artifacts"). The directory is created with mode 0o700.
func New(root string) (*Store, error) {
	if root == "" {
		return nil, fmt.Errorf("artifacts: empty root")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("artifacts: create %q: %w", root, err)
	}
	return &Store{root: root}, nil
}

// Root returns the store root directory.
func (s *Store) Root() string { return s.root }

// Write stores content (deduplicated by SHA-256) and returns the hex-encoded
// hash and the on-disk path. If the content already exists the existing file is
// returned unchanged (idempotent). The write is atomic: a temp file is written
// then renamed into place, so partial writes are never observable.
func (s *Store) Write(content []byte) (hash, path string, err error) {
	if content == nil {
		return "", "", fmt.Errorf("artifacts: nil content")
	}
	sum := sha256.Sum256(content)
	hash = hex.EncodeToString(sum[:])
	path = s.Path(hash)

	s.mu.Lock()
	defer s.mu.Unlock()

	if exists, err := fileExists(path); err != nil {
		return "", "", fmt.Errorf("artifacts: stat %q: %w", path, err)
	} else if exists {
		return hash, path, nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", "", fmt.Errorf("artifacts: mkdir %q: %w", filepath.Dir(path), err)
	}

	// Atomic write: temp file in the same dir, then rename.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return "", "", fmt.Errorf("artifacts: tmp create: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		cleanup()
		return "", "", fmt.Errorf("artifacts: tmp write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return "", "", fmt.Errorf("artifacts: tmp sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", "", fmt.Errorf("artifacts: tmp close: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return "", "", fmt.Errorf("artifacts: rename: %w", err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		// Non-fatal: the file is in place; read-only is best-effort.
		_ = err
	}
	return hash, path, nil
}

// WriteReader stores the bytes drained from r and returns the hash/path.
func (s *Store) WriteReader(r io.Reader) (hash, path string, err error) {
	content, err := io.ReadAll(r)
	if err != nil {
		return "", "", fmt.Errorf("artifacts: read: %w", err)
	}
	return s.Write(content)
}

// Path returns the canonical on-disk path for a hash (does not imply existence).
// The two-level sharding (<root>/ab/cdef...) keeps any single directory small.
func (s *Store) Path(hash string) string {
	if len(hash) < 4 {
		return filepath.Join(s.root, hash)
	}
	return filepath.Join(s.root, hash[:2], hash[2:])
}

// Read returns the content for a hash, or [ErrInvalidHash] if missing.
func (s *Store) Read(hash string) ([]byte, error) {
	path := s.Path(hash)
	if exists, err := fileExists(path); err != nil {
		return nil, fmt.Errorf("artifacts: stat %q: %w", path, err)
	} else if !exists {
		return nil, ErrInvalidHash
	}
	return os.ReadFile(path)
}

// Exists reports whether content for hash is present.
func (s *Store) Exists(hash string) bool {
	exists, err := fileExists(s.Path(hash))
	return err == nil && exists
}

// Size returns the byte length of a stored artifact, or -1 if missing.
func (s *Store) Size(hash string) int64 {
	if fi, err := os.Stat(s.Path(hash)); err == nil {
		return fi.Size()
	}
	return -1
}

// Hash computes the SHA-256 of content without storing it (useful for tests and
// for deduplication checks against an external reference).
func Hash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func fileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}
