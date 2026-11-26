package tui

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/012e/dcron/internal/config"
	"github.com/012e/dcron/internal/daemon"
	"github.com/012e/dcron/internal/registry"
	"github.com/012e/dcron/internal/runner"
	"github.com/012e/dcron/internal/schedule"
	"github.com/012e/dcron/internal/state"
)

// Deps holds all dependencies injected into the TUI.
type Deps struct {
	Registry        registry.Store
	Loader          config.Loader
	Parser          schedule.Parser
	Runner          runner.Runner
	StateStore      state.Store
	ProcessChecker  state.ProcessChecker
	ServiceManager  daemon.ServiceManager
	LogDir          string
	WorkDir         func() (string, error)
	RefreshInterval time.Duration
	InitialRepo     string
}

// JobStatus classifies the display status of a job.
type JobStatus int

const (
	StatusIdle JobStatus = iota
	StatusRunning
	StatusOK
	StatusFailed
	StatusDisabled
)

func (s JobStatus) Label() string {
	switch s {
	case StatusRunning:
		return "running"
	case StatusOK:
		return "ok"
	case StatusFailed:
		return "failed"
	case StatusDisabled:
		return "disabled"
	default:
		return "idle"
	}
}

// JobItem combines static configuration from .cron.toml with live daemon telemetry.
type JobItem struct {
	RepoName        string
	RepoPath        string
	Name            string
	Description     string
	Command         string
	WorkDir         string
	Shell           string
	Timeout         time.Duration
	Overlap         config.OverlapPolicy
	Env             map[string]string
	Enabled         bool
	ScheduleRaw     string
	ScheduleSummary string
	ScheduleKind    string
	Schedule        *schedule.Schedule
	LogPath         string

	NextRun        *time.Time
	LastRun        *time.Time
	LastDurationMs int64
	LastExitCode   *int
	LastError      string
	RunCount       int
	Running        bool
	LastBootID     string

	HasConfig bool
	HasState  bool
}

// Key uniquely identifies a job across repositories.
func (j JobItem) Key() string {
	return j.RepoPath + "\x00" + j.Name
}

// DisplayID returns a human-readable "repo/job" identifier.
func (j JobItem) DisplayID() string {
	return j.RepoName + "/" + j.Name
}

// EffectiveStatus returns the current status, accounting for in-TUI manual runs.
func (j JobItem) EffectiveStatus(manualRunning bool) JobStatus {
	if manualRunning || j.Running {
		return StatusRunning
	}
	if !j.Enabled {
		return StatusDisabled
	}
	if j.LastError != "" {
		return StatusFailed
	}
	if j.LastExitCode != nil {
		if *j.LastExitCode != 0 {
			return StatusFailed
		}
		return StatusOK
	}
	return StatusIdle
}

// ConfigJob reconstructs a runnable config.Job from a JobItem when config is loaded.
func (j JobItem) ConfigJob() (config.Job, bool) {
	if !j.HasConfig || j.Command == "" {
		return config.Job{}, false
	}
	var sched schedule.Schedule
	if j.Schedule != nil {
		sched = *j.Schedule
	}
	return config.Job{
		Name:        j.Name,
		Description: j.Description,
		Schedule:    sched,
		Command:     j.Command,
		Enabled:     j.Enabled,
		Timeout:     j.Timeout,
		Overlap:     j.Overlap,
		Shell:       j.Shell,
		WorkDir:     j.WorkDir,
		Env:         j.Env,
	}, true
}

// RepoItem represents one registered repository with its merged jobs and status summary.
type RepoItem struct {
	Entry        registry.Entry
	ConfigPath   string
	ConfigError  string
	DaemonError  string
	LoadedAt     time.Time
	Jobs         []JobItem
	RunningCount int
	FailedCount  int
	EnabledCount int
}

// HasError reports whether the repository currently has a live or daemon config error.
func (r RepoItem) HasError() bool {
	return r.ConfigError != "" || r.DaemonError != ""
}

// Snapshot is an immutable point-in-time view of daemon, registry, and job status.
type Snapshot struct {
	LoadedAt        time.Time
	DaemonAlive     bool
	DaemonPID       int
	DaemonStartedAt time.Time
	BootID          string
	Repos           []RepoItem
	AllJobs         []JobItem
	TotalRunning    int
	TotalFailed     int
	TotalErrors     int
}

// LoadSnapshot reads registry, daemon state, and each repository's .cron.toml into a Snapshot.
func LoadSnapshot(deps Deps, now time.Time) (Snapshot, error) {
	snap := Snapshot{
		LoadedAt: now,
	}

	var daemonSnap state.DaemonSnapshot
	if deps.StateStore != nil {
		loaded, err := deps.StateStore.Load()
		if err != nil {
			return snap, fmt.Errorf("load state: %w", err)
		}
		daemonSnap = loaded
	}

	snap.DaemonPID = daemonSnap.PID
	snap.DaemonStartedAt = daemonSnap.StartedAt
	snap.BootID = daemonSnap.BootID
	if deps.ProcessChecker != nil && daemonSnap.PID > 0 {
		snap.DaemonAlive = deps.ProcessChecker.IsAlive(daemonSnap.PID)
	}

	var entries []registry.Entry
	if deps.Registry != nil {
		var err error
		entries, err = deps.Registry.List()
		if err != nil {
			return snap, fmt.Errorf("list registry: %w", err)
		}
	}

	snap.Repos = make([]RepoItem, 0, len(entries))
	for _, entry := range entries {
		cfgPath := filepath.Join(entry.Path, config.FileName)
		if deps.Loader != nil {
			cfgPath = deps.Loader.ConfigPath(entry.Path)
		}

		repoItem := RepoItem{
			Entry:      entry,
			ConfigPath: cfgPath,
		}

		rs, hasRepoState := daemonSnap.Repos[entry.Path]
		if hasRepoState {
			repoItem.LoadedAt = rs.LoadedAt
			repoItem.DaemonError = rs.ConfigError
		}

		var cfg *config.RepoConfig
		if deps.Loader != nil {
			loadedCfg, err := deps.Loader.Load(entry.Path)
			if err != nil {
				repoItem.ConfigError = err.Error()
			} else {
				cfg = loadedCfg
			}
		}

		// Collect union of job names from config and state.
		nameSet := make(map[string]struct{})
		if cfg != nil {
			for name := range cfg.Jobs {
				nameSet[name] = struct{}{}
			}
		}
		if hasRepoState {
			for name := range rs.Jobs {
				nameSet[name] = struct{}{}
			}
		}
		names := make([]string, 0, len(nameSet))
		for name := range nameSet {
			names = append(names, name)
		}
		slices.Sort(names)

		repoItem.Jobs = make([]JobItem, 0, len(names))
		for _, jn := range names {
			item := JobItem{
				RepoName: entry.Name,
				RepoPath: entry.Path,
				Name:     jn,
			}

			if cfg != nil {
				if cj, ok := cfg.Jobs[jn]; ok {
					item.HasConfig = true
					item.Description = cj.Description
					item.Command = cj.Command
					item.WorkDir = cj.WorkDir
					item.Shell = cj.Shell
					item.Timeout = cj.Timeout
					item.Overlap = cj.Overlap
					item.Env = cj.Env
					item.Enabled = cj.Enabled
					item.ScheduleRaw = cj.Schedule.Raw
					item.ScheduleSummary = cj.Schedule.Summary()
					item.ScheduleKind = cj.Schedule.Kind.String()
					schedCopy := cj.Schedule
					item.Schedule = &schedCopy
				}
			}

			if hasRepoState {
				if js, ok := rs.Jobs[jn]; ok {
					item.HasState = true
					if !item.HasConfig {
						item.Description = js.Description
						item.Enabled = js.Enabled
						item.ScheduleRaw = js.ScheduleRaw
						item.ScheduleSummary = js.ScheduleSummary
						item.ScheduleKind = js.ScheduleKind
					}
					item.NextRun = js.NextRun
					item.LastRun = js.LastRun
					item.LastDurationMs = js.LastDurationMs
					item.LastExitCode = js.LastExitCode
					item.LastError = js.LastError
					item.RunCount = js.RunCount
					item.Running = snap.DaemonAlive && js.Running
					item.LastBootID = js.LastBootID
					item.LogPath = js.LogPath
				}
			}

			if item.LogPath == "" && deps.LogDir != "" {
				item.LogPath = runner.LogPathFor(deps.LogDir, entry.Name, entry.Path, jn)
			}

			if item.Enabled {
				repoItem.EnabledCount++
			}
			switch item.EffectiveStatus(false) {
			case StatusRunning:
				repoItem.RunningCount++
				snap.TotalRunning++
			case StatusFailed:
				repoItem.FailedCount++
				snap.TotalFailed++
			}

			repoItem.Jobs = append(repoItem.Jobs, item)
			snap.AllJobs = append(snap.AllJobs, item)
		}

		if repoItem.HasError() {
			snap.TotalErrors++
		}
		snap.Repos = append(snap.Repos, repoItem)
	}

	return snap, nil
}

// ReadLogTail reads up to the last maxLines lines from path.
// If the file does not exist, exists is false and err is nil.
func ReadLogTail(path string, maxLines int) (content string, exists bool, err error) {
	if strings.TrimSpace(path) == "" {
		return "", false, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	defer func() { _ = f.Close() }()

	if maxLines <= 0 {
		maxLines = 500
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	ring := make([]string, 0, maxLines)
	for scanner.Scan() {
		line := scanner.Text()
		if len(ring) < maxLines {
			ring = append(ring, line)
		} else {
			copy(ring, ring[1:])
			ring[maxLines-1] = line
		}
	}
	if err := scanner.Err(); err != nil {
		return "", true, err
	}
	return strings.Join(ring, "\n"), true, nil
}

// FormatRelativeTime renders t relative to now in compact human-readable form.
func FormatRelativeTime(t, now time.Time) string {
	d := t.Sub(now)
	if d >= 0 {
		if d < time.Second {
			return "now"
		}
		return "in " + formatCompactDuration(d)
	}
	d = -d
	if d < 2*time.Second {
		return "just now"
	}
	return formatCompactDuration(d) + " ago"
}

// FormatDurationMs formats a duration in milliseconds compactly.
func FormatDurationMs(ms int64) string {
	if ms <= 0 {
		return "<1ms"
	}
	d := time.Duration(ms) * time.Millisecond
	if d < time.Second {
		return fmt.Sprintf("%dms", ms)
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return formatCompactDuration(d)
}

func formatCompactDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		m := int(d / time.Minute)
		s := int((d % time.Minute) / time.Second)
		if s == 0 || m >= 10 {
			return fmt.Sprintf("%dm", m)
		}
		return fmt.Sprintf("%dm%ds", m, s)
	}
	if d < 24*time.Hour {
		h := int(d / time.Hour)
		m := int((d % time.Hour) / time.Minute)
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%dm", h, m)
	}
	days := int(d / (24 * time.Hour))
	h := int((d % (24 * time.Hour)) / time.Hour)
	if h == 0 {
		return fmt.Sprintf("%dd", days)
	}
	return fmt.Sprintf("%dd%dh", days, h)
}
