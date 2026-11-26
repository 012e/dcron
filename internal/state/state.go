package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// JobState captures runtime telemetry for a single job.
type JobState struct {
	Name            string     `json:"name"`
	Description     string     `json:"description,omitempty"`
	ScheduleRaw     string     `json:"schedule_raw"`
	ScheduleSummary string     `json:"schedule_summary"`
	ScheduleKind    string     `json:"schedule_kind"`
	Enabled         bool       `json:"enabled"`
	NextRun         *time.Time `json:"next_run,omitempty"`
	LastRun         *time.Time `json:"last_run,omitempty"`
	LastDurationMs  int64      `json:"last_duration_ms,omitempty"`
	LastExitCode    *int       `json:"last_exit_code,omitempty"`
	LastError       string     `json:"last_error,omitempty"`
	RunCount        int        `json:"run_count"`
	Running         bool       `json:"running"`
	LastBootID      string     `json:"last_boot_id,omitempty"`
	LogPath         string     `json:"log_path,omitempty"`
}

// RepoState captures the loaded status and jobs of a single registered repository.
type RepoState struct {
	Name        string              `json:"name"`
	Path        string              `json:"path"`
	LoadedAt    time.Time           `json:"loaded_at"`
	ConfigError string              `json:"config_error,omitempty"`
	Jobs        map[string]JobState `json:"jobs"`
}

// DaemonSnapshot is the full persisted status written by the daemon and read by CLI commands.
type DaemonSnapshot struct {
	PID       int                  `json:"pid"`
	StartedAt time.Time            `json:"started_at"`
	UpdatedAt time.Time            `json:"updated_at"`
	BootID    string               `json:"boot_id,omitempty"`
	Repos     map[string]RepoState `json:"repos"`
}

// Clone returns a deep copy of the snapshot safe for serialization or inspection.
func (s DaemonSnapshot) Clone() DaemonSnapshot {
	out := s
	out.Repos = make(map[string]RepoState, len(s.Repos))
	for rp, rs := range s.Repos {
		rc := rs
		rc.Jobs = make(map[string]JobState, len(rs.Jobs))
		for jn, js := range rs.Jobs {
			jc := js
			if js.NextRun != nil {
				t := *js.NextRun
				jc.NextRun = &t
			}
			if js.LastRun != nil {
				t := *js.LastRun
				jc.LastRun = &t
			}
			if js.LastExitCode != nil {
				c := *js.LastExitCode
				jc.LastExitCode = &c
			}
			rc.Jobs[jn] = jc
		}
		out.Repos[rp] = rc
	}
	return out
}

// Store persists and loads DaemonSnapshot.
type Store interface {
	Load() (DaemonSnapshot, error)
	Save(DaemonSnapshot) error
	Path() string
}

// BootIDProvider returns an identifier that changes across OS reboots.
type BootIDProvider interface {
	BootID() (string, error)
}

// ProcessChecker reports whether a local OS process is alive.
type ProcessChecker interface {
	IsAlive(pid int) bool
}

// DefaultStateDir returns the XDG state directory for dcron.
func DefaultStateDir() (string, error) {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "dcron"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".local", "state", "dcron"), nil
}

// FileStore persists DaemonSnapshot as JSON using atomic temp-file rename.
type FileStore struct {
	mu   sync.Mutex
	path string
}

// NewFileStore constructs a FileStore at path.
func NewFileStore(path string) *FileStore {
	return &FileStore{path: path}
}

func (s *FileStore) Path() string {
	return s.path
}

// Load reads the persisted snapshot. Returns an empty initialized snapshot if the file does not exist.
func (s *FileStore) Load() (DaemonSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DaemonSnapshot{Repos: make(map[string]RepoState)}, nil
		}
		return DaemonSnapshot{}, fmt.Errorf("read state %s: %w", s.path, err)
	}

	var snap DaemonSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return DaemonSnapshot{}, fmt.Errorf("decode state %s: %w", s.path, err)
	}
	if snap.Repos == nil {
		snap.Repos = make(map[string]RepoState)
	}
	return snap, nil
}

// Save atomically persists snap as formatted JSON.
func (s *FileStore) Save(snap DaemonSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create state dir %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp state file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp state file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp state file: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return fmt.Errorf("chmod temp state file: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("replace state %s: %w", s.path, err)
	}
	cleanup = false
	return nil
}

// LinuxBootIDProvider reads /proc/sys/kernel/random/boot_id.
type LinuxBootIDProvider struct {
	Path string
}

// NewLinuxBootIDProvider returns a provider reading the Linux kernel boot_id.
func NewLinuxBootIDProvider() *LinuxBootIDProvider {
	return &LinuxBootIDProvider{Path: "/proc/sys/kernel/random/boot_id"}
}

func (p *LinuxBootIDProvider) BootID() (string, error) {
	path := p.Path
	if path == "" {
		path = "/proc/sys/kernel/random/boot_id"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// OSProcessChecker checks process liveness using signal 0.
type OSProcessChecker struct{}

func (OSProcessChecker) IsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
