package registry

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
)

var (
	ErrAlreadyRegistered = errors.New("repository is already registered")
	ErrNotFound          = errors.New("repository not found in registry")
)

// Entry represents a registered repository directory.
type Entry struct {
	Name    string    `toml:"name"`
	Path    string    `toml:"path"`
	AddedAt time.Time `toml:"added_at"`
}

type registryFile struct {
	Repos []Entry `toml:"repos"`
}

// Store manages the persistent set of registered repositories.
type Store interface {
	List() ([]Entry, error)
	Add(path string, name string) (Entry, error)
	Remove(pathOrName string) (Entry, error)
	Find(pathOrName string) (Entry, bool, error)
	Path() string
}

// DefaultPath returns the platform-appropriate path for registry.toml.
func DefaultPath() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "dcron", "registry.toml"), nil
	}
	if runtime.GOOS == "windows" {
		if cfgDir, err := os.UserConfigDir(); err == nil && cfgDir != "" {
			return filepath.Join(cfgDir, "dcron", "registry.toml"), nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".config", "dcron", "registry.toml"), nil
}

// CanonicalPath returns an absolute, symlink-resolved path when possible.
func CanonicalPath(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", errors.New("path cannot be empty")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("resolve absolute path %q: %w", p, err)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved), nil
	}
	return filepath.Clean(abs), nil
}

// FileStore persists registered repositories to a TOML file using atomic renames.
type FileStore struct {
	mu   sync.Mutex
	path string
	now  func() time.Time
}

// NewFileStore constructs a FileStore backed by path.
func NewFileStore(path string) *FileStore {
	return &FileStore{
		path: path,
		now:  time.Now,
	}
}

// Path returns the underlying registry file path.
func (s *FileStore) Path() string {
	return s.path
}

// List returns all registered repositories sorted by name.
func (s *FileStore) List() ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readLocked()
}

// Add registers a repository directory. If name is empty, a unique name derived
// from the directory basename is assigned.
func (s *FileStore) Add(path string, name string) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	canon, err := CanonicalPath(path)
	if err != nil {
		return Entry{}, err
	}
	info, err := os.Stat(canon)
	if err != nil {
		return Entry{}, fmt.Errorf("stat %s: %w", canon, err)
	}
	if !info.IsDir() {
		return Entry{}, fmt.Errorf("%s is not a directory", canon)
	}

	entries, err := s.readLocked()
	if err != nil {
		return Entry{}, err
	}

	for _, e := range entries {
		if pathsEqual(e.Path, canon) {
			return Entry{}, fmt.Errorf("%w: %s (%s)", ErrAlreadyRegistered, e.Name, e.Path)
		}
	}

	chosenName := strings.TrimSpace(name)
	if chosenName == "" {
		chosenName = uniqueName(filepath.Base(canon), entries)
	} else {
		for _, e := range entries {
			if e.Name == chosenName {
				return Entry{}, fmt.Errorf("repository name %q is already in use by %s", chosenName, e.Path)
			}
		}
	}

	entry := Entry{
		Name:    chosenName,
		Path:    canon,
		AddedAt: s.now().UTC().Truncate(time.Second),
	}
	entries = append(entries, entry)
	sortEntries(entries)

	if err := s.writeLocked(entries); err != nil {
		return Entry{}, err
	}
	return entry, nil
}

// Remove unregisters a repository matched by name or path.
func (s *FileStore) Remove(pathOrName string) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.readLocked()
	if err != nil {
		return Entry{}, err
	}

	idx := findEntryIndex(entries, pathOrName)
	if idx < 0 {
		return Entry{}, fmt.Errorf("%w: %s", ErrNotFound, pathOrName)
	}

	removed := entries[idx]
	entries = append(entries[:idx], entries[idx+1:]...)
	if err := s.writeLocked(entries); err != nil {
		return Entry{}, err
	}
	return removed, nil
}

// Find looks up a repository by name or path.
func (s *FileStore) Find(pathOrName string) (Entry, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.readLocked()
	if err != nil {
		return Entry{}, false, err
	}
	idx := findEntryIndex(entries, pathOrName)
	if idx < 0 {
		return Entry{}, false, nil
	}
	return entries[idx], true, nil
}

func (s *FileStore) readLocked() ([]Entry, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read registry %s: %w", s.path, err)
	}

	var rf registryFile
	if _, err := toml.Decode(string(data), &rf); err != nil {
		return nil, fmt.Errorf("decode registry %s: %w", s.path, err)
	}
	sortEntries(rf.Repos)
	return rf.Repos, nil
}

func (s *FileStore) writeLocked(entries []Entry) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create registry dir %s: %w", dir, err)
	}

	var buf bytes.Buffer
	enc := toml.NewEncoder(&buf)
	if err := enc.Encode(registryFile{Repos: entries}); err != nil {
		return fmt.Errorf("encode registry: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".registry-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp registry file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(buf.Bytes()); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp registry file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp registry file: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return fmt.Errorf("chmod temp registry file: %w", err)
	}
	if err := renameFile(tmpPath, s.path); err != nil {
		return fmt.Errorf("replace registry %s: %w", s.path, err)
	}
	cleanup = false
	return nil
}

func renameFile(src, dst string) error {
	attempts := 1
	if runtime.GOOS == "windows" {
		attempts = 5
	}
	var err error
	for i := range attempts {
		err = os.Rename(src, dst)
		if err == nil {
			return nil
		}
		if i+1 < attempts {
			time.Sleep(time.Duration(i+1) * 15 * time.Millisecond)
		}
	}
	return err
}

func pathsEqual(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func findEntryIndex(entries []Entry, query string) int {
	q := strings.TrimSpace(query)
	if q == "" {
		return -1
	}
	for i, e := range entries {
		if e.Name == q || pathsEqual(e.Path, q) {
			return i
		}
	}
	if canon, err := CanonicalPath(q); err == nil {
		for i, e := range entries {
			if pathsEqual(e.Path, canon) {
				return i
			}
		}
	}
	return -1
}

func uniqueName(base string, entries []Entry) string {
	used := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		used[e.Name] = struct{}{}
	}
	if _, exists := used[base]; !exists {
		return base
	}
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s-%d", base, i)
		if _, exists := used[cand]; !exists {
			return cand
		}
	}
}

func sortEntries(entries []Entry) {
	slices.SortFunc(entries, func(a, b Entry) int {
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.Path, b.Path)
	})
}
