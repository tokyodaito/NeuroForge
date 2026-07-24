package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDefaultDirs_HomeFallback verifies the runtime layout resolves to a home
// directory and produces absolute paths on every platform (Windows uses
// os.UserHomeDir -> filepath.Join, not a hard-coded Unix path).
func TestDefaultDirs_HomeFallback(t *testing.T) {
	t.Setenv(EnvHome, "")
	d, err := DefaultDirs()
	if err != nil {
		t.Fatalf("DefaultDirs: %v", err)
	}
	if d.Root == "" {
		t.Fatal("empty root")
	}
	if !filepath.IsAbs(d.Root) {
		t.Errorf("root is not absolute: %s", d.Root)
	}
	for _, p := range []string{
		d.StateDB, d.LogFile, d.PIDFile, d.TokenFile, d.AddrFile, d.ArtifactsDir,
	} {
		if !filepath.IsAbs(p) {
			t.Errorf("path not absolute: %s", p)
		}
	}
}

// TestDefaultDirs_EnvHomeOverride verifies NEUROFORGE_HOME overrides the default
// home and is made absolute.
func TestDefaultDirs_EnvHomeOverride(t *testing.T) {
	base := t.TempDir()
	rel := filepath.Join(base, "nf-home")
	t.Setenv(EnvHome, rel)
	d, err := DefaultDirs()
	if err != nil {
		t.Fatalf("DefaultDirs: %v", err)
	}
	if !filepath.IsAbs(d.Root) {
		t.Errorf("root not absolute: %s", d.Root)
	}
	if filepath.Separator != '/' && filepath.Base(d.Root) != "nf-home" {
		t.Errorf("root base = %q, want nf-home", filepath.Base(d.Root))
	}
}

// TestDirs_Ensure_PathWithSpaces verifies the runtime directory tree is created
// under a root whose path contains spaces. This is common on Windows (e.g.
// "C:\Users\Some User\.neuroforge") and must not break path handling.
func TestDirs_Ensure_PathWithSpaces(t *testing.T) {
	base := t.TempDir()
	homeWithSpaces := filepath.Join(base, "has space dir")
	d := WithRoot(homeWithSpaces)
	if err := d.Ensure(); err != nil {
		t.Fatalf("Ensure under a path with spaces: %v", err)
	}
	if _, err := os.Stat(d.Root); err != nil {
		t.Errorf("root not created: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(d.PIDFile)); err != nil {
		t.Errorf("run dir not created: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(d.LogFile)); err != nil {
		t.Errorf("log dir not created: %v", err)
	}
}
