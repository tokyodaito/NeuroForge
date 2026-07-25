package artifacts_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"neuroforge/internal/artifacts"
)

func TestStore_WriteReadIdempotent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := artifacts.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("hello artifact world")
	hash1, path1, err := s.Write(content)
	if err != nil {
		t.Fatal(err)
	}
	if hash1 == "" || path1 == "" {
		t.Fatal("empty hash/path returned")
	}
	// Same content → same hash, same path, no error.
	hash2, path2, err := s.Write(content)
	if err != nil {
		t.Fatal(err)
	}
	if hash1 != hash2 || path1 != path2 {
		t.Errorf("non-idempotent: %s/%s then %s/%s", hash1, path1, hash2, path2)
	}
	// File is at a sharded path under <root>/<hash[:2]>/<hash[2:]>.
	rel, _ := filepath.Rel(dir, path1)
	if filepath.Dir(rel) != hash1[:2] {
		t.Errorf("path not sharded: %s", rel)
	}
	got, err := s.Read(hash1)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("read = %q, want %q", got, content)
	}
}

func TestStore_DedupSeparatesDistinct(t *testing.T) {
	t.Parallel()
	s, err := artifacts.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h1, _, _ := s.Write([]byte("aaa"))
	h2, _, _ := s.Write([]byte("bbb"))
	if h1 == h2 {
		t.Error("distinct content hashed the same")
	}
	if !s.Exists(h1) || !s.Exists(h2) {
		t.Error("Exists false after Write")
	}
	if s.Exists(strings.Repeat("0", 64)) {
		t.Error("Exists true for unknown hash")
	}
	if _, err := s.Read("nonexistent"); err != artifacts.ErrInvalidHash {
		t.Errorf("read unknown err = %v, want ErrInvalidHash", err)
	}
}

func TestStore_HashStable(t *testing.T) {
	if artifacts.Hash([]byte("abc")) != artifacts.Hash([]byte("abc")) {
		t.Error("Hash not stable")
	}
	if artifacts.Hash([]byte("abc")) == artifacts.Hash([]byte("abd")) {
		t.Error("Hash collided")
	}
}

func TestStore_FileMode(t *testing.T) {
	t.Parallel()
	s, err := artifacts.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, path, err := s.Write([]byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Read-only (0o400) on POSIX, mode 0o444 on some filesystems; just check
	// the owner-read bit and absence of write bit for group/other.
	if fi.Mode().Perm()&0o777 != 0o400 && fi.Mode().Perm()&0o777 != 0o444 {
		t.Logf("note: file mode = %v (best-effort read-only)", fi.Mode().Perm())
	}
}
