package registry

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFileStore_CRUD(t *testing.T) {
	tmp := t.TempDir()
	store := NewFileStore(filepath.Join(tmp, "config", "registry.toml"))

	// Empty initially.
	list, err := store.List()
	if err != nil {
		t.Fatalf("List() empty: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("got %d entries, want 0", len(list))
	}

	repo1 := filepath.Join(tmp, "work", "api")
	repo2 := filepath.Join(tmp, "personal", "api")
	for _, d := range []string{repo1, repo2} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Add first repo.
	e1, err := store.Add(repo1, "")
	if err != nil {
		t.Fatalf("Add(repo1): %v", err)
	}
	if e1.Name != "api" {
		t.Errorf("e1.Name = %q, want api", e1.Name)
	}

	// Duplicate path rejected.
	if _, err := store.Add(repo1, "other"); !errors.Is(err, ErrAlreadyRegistered) {
		t.Fatalf("expected ErrAlreadyRegistered, got %v", err)
	}

	// Same basename in different folder auto-disambiguates.
	e2, err := store.Add(repo2, "")
	if err != nil {
		t.Fatalf("Add(repo2): %v", err)
	}
	if e2.Name != "api-2" {
		t.Errorf("e2.Name = %q, want api-2", e2.Name)
	}

	// Find by name and by path.
	got, ok, err := store.Find("api-2")
	if err != nil || !ok || got.Path != e2.Path {
		t.Fatalf("Find(api-2) = (%+v, %v, %v)", got, ok, err)
	}
	got, ok, err = store.Find(repo1)
	if err != nil || !ok || got.Name != "api" {
		t.Fatalf("Find(repo1) = (%+v, %v, %v)", got, ok, err)
	}

	// Remove by name.
	removed, err := store.Remove("api")
	if err != nil {
		t.Fatalf("Remove(api): %v", err)
	}
	if removed.Path != e1.Path {
		t.Errorf("removed.Path = %q, want %q", removed.Path, e1.Path)
	}

	// Removing missing returns ErrNotFound.
	if _, err := store.Remove("api"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	list, err = store.List()
	if err != nil || len(list) != 1 || list[0].Name != "api-2" {
		t.Fatalf("final List() = (%+v, %v)", list, err)
	}
}

func TestFileStore_ReservedGlobalName(t *testing.T) {
	tmp := t.TempDir()
	store := NewFileStore(filepath.Join(tmp, "config", "registry.toml"))

	dirA := filepath.Join(tmp, "myrepo")
	dirGlobal := filepath.Join(tmp, "global")
	for _, d := range []string{dirA, dirGlobal} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := store.Add(dirA, "global"); !errors.Is(err, ErrReservedName) {
		t.Fatalf("expected ErrReservedName for explicit 'global', got %v", err)
	}
	if _, err := store.Add(dirA, "GLOBAL"); !errors.Is(err, ErrReservedName) {
		t.Fatalf("expected ErrReservedName for explicit 'GLOBAL', got %v", err)
	}

	// Auto-derived name for a directory literally named "global" should skip the reserved name.
	entry, err := store.Add(dirGlobal, "")
	if err != nil {
		t.Fatalf("Add(dirGlobal, '') unexpected error: %v", err)
	}
	if entry.Name != "global-2" {
		t.Errorf("auto-derived name = %q, want global-2", entry.Name)
	}
}

func TestCanonicalPath_NestedNonExistentUnderSymlink(t *testing.T) {
	tmp := t.TempDir()
	realDir := filepath.Join(tmp, "real")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(tmp, "link")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Skipf("symlinks unavailable in test environment: %v", err)
	}

	resolvedReal, err := filepath.EvalSymlinks(realDir)
	if err != nil {
		t.Fatal(err)
	}

	nested := filepath.Join(linkDir, "missing-config", "nested", "global.toml")
	got, err := CanonicalPath(nested)
	if err != nil {
		t.Fatalf("CanonicalPath(%q) error: %v", nested, err)
	}
	want := filepath.Join(resolvedReal, "missing-config", "nested", "global.toml")
	if got != want {
		t.Errorf("CanonicalPath(%q) = %q, want %q", nested, got, want)
	}
}
