package config

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/012e/dcron/internal/schedule"
)

// FileName is the expected configuration filename at the root of a repository.
const FileName = ".cron.toml"

// GlobalFileName is the filename for user-wide global cron jobs in the dcron config directory.
const GlobalFileName = "global.toml"

// GlobalRepoName is the reserved display/reference name for global cron jobs.
const GlobalRepoName = "global"

// DefaultGlobalPath returns the platform-appropriate path for global.toml.
func DefaultGlobalPath() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "dcron", GlobalFileName), nil
	}
	if runtime.GOOS == "windows" {
		if cfgDir, err := os.UserConfigDir(); err == nil && cfgDir != "" {
			return filepath.Join(cfgDir, "dcron", GlobalFileName), nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".config", "dcron", GlobalFileName), nil
}

// OverlapPolicy controls what happens if a job is triggered while a previous run is still active.
type OverlapPolicy string

const (
	OverlapSkip  OverlapPolicy = "skip"
	OverlapAllow OverlapPolicy = "allow"
	OverlapWait  OverlapPolicy = "wait"
)

// Defaults provides repo-wide default settings inherited by every job unless overridden.
type Defaults struct {
	Shell   string            `toml:"shell"`
	Timeout string            `toml:"timeout"`
	Overlap OverlapPolicy     `toml:"overlap"`
	Dir     string            `toml:"dir"`
	Env     map[string]string `toml:"env"`
}

// RawJob represents a single job entry as decoded from TOML.
type RawJob struct {
	Description string            `toml:"description"`
	Schedule    string            `toml:"schedule"`
	Command     string            `toml:"command"`
	Enabled     *bool             `toml:"enabled"`
	Timeout     string            `toml:"timeout"`
	Overlap     OverlapPolicy     `toml:"overlap"`
	Shell       string            `toml:"shell"`
	Dir         string            `toml:"dir"`
	Env         map[string]string `toml:"env"`
}

type rawFile struct {
	Defaults Defaults          `toml:"defaults"`
	Jobs     map[string]RawJob `toml:"jobs"`
}

// Job is a fully resolved and validated job definition ready to schedule or execute.
type Job struct {
	Name        string
	Description string
	Schedule    schedule.Schedule
	Command     string
	Enabled     bool
	Timeout     time.Duration
	Overlap     OverlapPolicy
	Shell       string
	WorkDir     string
	Env         map[string]string
}

// RepoConfig is the parsed, validated configuration for a single repository or global config file.
type RepoConfig struct {
	RepoPath   string
	ConfigPath string
	Defaults   Defaults
	Jobs       map[string]Job
}

// SortedJobNames returns the job names in deterministic alphabetical order.
func (c *RepoConfig) SortedJobNames() []string {
	names := make([]string, 0, len(c.Jobs))
	for name := range c.Jobs {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// Loader loads and validates repo-local and global cron configurations.
type Loader interface {
	Load(repoPath string) (*RepoConfig, error)
	LoadFile(cfgPath string, defaultWorkDir string) (*RepoConfig, error)
	ConfigPath(repoPath string) string
}

// TOMLLoader loads .cron.toml and global.toml files and validates job schedules using Parser.
type TOMLLoader struct {
	parser       schedule.Parser
	defaultShell string
	homeDir      func() (string, error)
}

// NewTOMLLoader returns a TOMLLoader backed by the given schedule parser.
func NewTOMLLoader(parser schedule.Parser) *TOMLLoader {
	return &TOMLLoader{
		parser:       parser,
		defaultShell: resolveDefaultShell(runtime.GOOS, os.Getenv),
		homeDir:      os.UserHomeDir,
	}
}

func resolveDefaultShell(goos string, getenv func(string) string) string {
	if goos == "windows" {
		if comspec := getenv("COMSPEC"); comspec != "" {
			return comspec
		}
		return "cmd.exe"
	}
	if shell := getenv("SHELL"); shell != "" {
		return shell
	}
	return "/bin/sh"
}

// ConfigPath returns the full path to .cron.toml for repoPath.
func (l *TOMLLoader) ConfigPath(repoPath string) string {
	return filepath.Join(repoPath, FileName)
}

// Load reads, decodes, and validates <repoPath>/.cron.toml.
func (l *TOMLLoader) Load(repoPath string) (*RepoConfig, error) {
	if repoPath == "" {
		return nil, errors.New("repo path cannot be empty")
	}
	absRepo, err := filepath.Abs(repoPath)
	if err != nil {
		return nil, fmt.Errorf("resolve repo path: %w", err)
	}

	cfgPath := l.ConfigPath(absRepo)
	return l.loadFile(cfgPath, absRepo, absRepo)
}

// LoadFile reads, decodes, and validates an explicit TOML configuration file (such as global.toml).
// When defaultWorkDir is empty, jobs default to running in the user's home directory (falling back
// to the configuration file's parent directory if the home directory cannot be resolved).
func (l *TOMLLoader) LoadFile(cfgPath string, defaultWorkDir string) (*RepoConfig, error) {
	if strings.TrimSpace(cfgPath) == "" {
		return nil, errors.New("config path cannot be empty")
	}
	absCfg, err := filepath.Abs(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}

	baseDir := strings.TrimSpace(defaultWorkDir)
	if baseDir != "" {
		expanded, err := l.expandHome(baseDir)
		if err != nil {
			return nil, fmt.Errorf("resolve default workdir: %w", err)
		}
		absBase, err := filepath.Abs(expanded)
		if err != nil {
			return nil, fmt.Errorf("resolve default workdir: %w", err)
		}
		baseDir = absBase
	} else {
		if home, err := l.resolveHomeDir(); err == nil && home != "" {
			baseDir = home
		} else {
			baseDir = filepath.Dir(absCfg)
		}
	}

	return l.loadFile(absCfg, absCfg, baseDir)
}

func (l *TOMLLoader) loadFile(cfgPath, repoPath, baseWorkDir string) (*RepoConfig, error) {
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", cfgPath, err)
	}

	var rf rawFile
	md, err := toml.Decode(string(data), &rf)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", cfgPath, err)
	}

	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		return nil, fmt.Errorf("unknown configuration key(s) in %s: %s", cfgPath, strings.Join(keys, ", "))
	}

	// Validate defaults.
	defaultOverlap := OverlapSkip
	if rf.Defaults.Overlap != "" {
		if err := validateOverlap(rf.Defaults.Overlap); err != nil {
			return nil, fmt.Errorf("defaults.overlap: %w", err)
		}
		defaultOverlap = rf.Defaults.Overlap
	}

	var defaultTimeout time.Duration
	if rf.Defaults.Timeout != "" {
		d, err := time.ParseDuration(rf.Defaults.Timeout)
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("defaults.timeout: invalid positive duration %q", rf.Defaults.Timeout)
		}
		defaultTimeout = d
	}

	defaultShell := l.defaultShell
	if rf.Defaults.Shell != "" {
		defaultShell = rf.Defaults.Shell
	}

	defaultDir := "."
	if rf.Defaults.Dir != "" {
		defaultDir = rf.Defaults.Dir
	}

	jobs := make(map[string]Job, len(rf.Jobs))
	for name, rj := range rf.Jobs {
		if strings.TrimSpace(name) == "" {
			return nil, errors.New("job name cannot be empty")
		}
		if strings.TrimSpace(rj.Schedule) == "" {
			return nil, fmt.Errorf("job %q: schedule is required", name)
		}
		if strings.TrimSpace(rj.Command) == "" {
			return nil, fmt.Errorf("job %q: command is required", name)
		}

		parsedSched, err := l.parser.Parse(rj.Schedule)
		if err != nil {
			return nil, fmt.Errorf("job %q: %w", name, err)
		}

		enabled := true
		if rj.Enabled != nil {
			enabled = *rj.Enabled
		}

		timeout := defaultTimeout
		if rj.Timeout != "" {
			d, err := time.ParseDuration(rj.Timeout)
			if err != nil || d <= 0 {
				return nil, fmt.Errorf("job %q: invalid positive timeout %q", name, rj.Timeout)
			}
			timeout = d
		}

		overlap := defaultOverlap
		if rj.Overlap != "" {
			if err := validateOverlap(rj.Overlap); err != nil {
				return nil, fmt.Errorf("job %q: %w", name, err)
			}
			overlap = rj.Overlap
		}

		shell := defaultShell
		if rj.Shell != "" {
			shell = rj.Shell
		}

		dir := defaultDir
		if rj.Dir != "" {
			dir = rj.Dir
		}
		workDir, err := l.resolveWorkDir(dir, baseWorkDir)
		if err != nil {
			return nil, fmt.Errorf("job %q: dir %q: %w", name, dir, err)
		}

		env := make(map[string]string, len(rf.Defaults.Env)+len(rj.Env))
		maps.Copy(env, rf.Defaults.Env)
		maps.Copy(env, rj.Env)

		jobs[name] = Job{
			Name:        name,
			Description: rj.Description,
			Schedule:    parsedSched,
			Command:     rj.Command,
			Enabled:     enabled,
			Timeout:     timeout,
			Overlap:     overlap,
			Shell:       shell,
			WorkDir:     workDir,
			Env:         env,
		}
	}

	return &RepoConfig{
		RepoPath:   repoPath,
		ConfigPath: cfgPath,
		Defaults:   rf.Defaults,
		Jobs:       jobs,
	}, nil
}

func (l *TOMLLoader) resolveHomeDir() (string, error) {
	fn := l.homeDir
	if fn == nil {
		fn = os.UserHomeDir
	}
	return fn()
}

func (l *TOMLLoader) expandHome(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		home, err := l.resolveHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand home directory: %w", err)
		}
		if p == "~" {
			return home, nil
		}
		return filepath.Join(home, p[2:]), nil
	}
	return p, nil
}

func (l *TOMLLoader) resolveWorkDir(dir, baseWorkDir string) (string, error) {
	expanded, err := l.expandHome(dir)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(expanded) {
		expanded = filepath.Join(baseWorkDir, expanded)
	}
	return filepath.Clean(expanded), nil
}

func validateOverlap(p OverlapPolicy) error {
	switch p {
	case OverlapSkip, OverlapAllow, OverlapWait:
		return nil
	default:
		return fmt.Errorf("invalid overlap policy %q (must be 'skip', 'allow', or 'wait')", p)
	}
}

// StarterTemplate returns a commented starter .cron.toml template for `dcron init`.
func StarterTemplate() string {
	return `# dcron repository configuration
# Docs: run "dcron validate" to check syntax and preview next run times.

[defaults]
# shell = "/bin/sh" # auto-detected per OS ($SHELL or /bin/sh on Unix, %COMSPEC%/cmd.exe on Windows)
timeout = "10m"
overlap = "skip" # skip | wait | allow

[jobs.sync]
description = "Run periodic sync script"
schedule = "every hour" # supports natural language ("daily at 9am", "every 15m", "on reboot") or 5-field cron ("0 * * * *")
command = "echo running sync"

# [jobs.boot_warmup]
# description = "Run once when the machine boots"
# schedule = "on reboot"
# command = "make warmup"
# env = { MODE = "local" }
`
}

// GlobalStarterTemplate returns a commented starter global.toml template for `dcron init --global`.
func GlobalStarterTemplate() string {
	return `# dcron global configuration (watched automatically by the daemon)
# Docs: run "dcron validate -g" to check syntax and preview next run times.
# Jobs run from your home directory by default; "dir" supports "~" and absolute paths.

[defaults]
# shell = "/bin/sh" # auto-detected per OS ($SHELL or /bin/sh on Unix, %COMSPEC%/cmd.exe on Windows)
# dir = "~"
timeout = "10m"
overlap = "skip" # skip | wait | allow

[jobs.heartbeat]
description = "Example hourly global job"
schedule = "every hour" # supports natural language ("daily at 9am", "every 15m", "on reboot") or 5-field cron ("0 * * * *")
command = "echo running global heartbeat"

# [jobs.boot_setup]
# description = "Run once when the machine boots"
# schedule = "on reboot"
# command = "echo machine booted"
# dir = "~"
`
}
