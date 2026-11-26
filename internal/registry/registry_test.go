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
