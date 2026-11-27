package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/012e/dcron/internal/config"
	"github.com/012e/dcron/internal/daemon"
	"github.com/012e/dcron/internal/registry"
	"github.com/012e/dcron/internal/runner"
	"github.com/012e/dcron/internal/schedule"
	"github.com/012e/dcron/internal/state"
	"github.com/012e/dcron/internal/tui"
)

// App holds the injected dependencies used by all CLI subcommands.
type App struct {
	Registry         registry.Store
	Loader           config.Loader
	Parser           schedule.Parser
	Runner           runner.Runner
	StateStore       state.Store
	ProcessChecker   state.ProcessChecker
	ServiceManager   daemon.ServiceManager
	Controller       daemon.Controller
	DaemonFactory    func() (daemon.Daemon, error)
	TUIRunner        func(ctx context.Context, deps tui.Deps) error
	GlobalConfigPath string
	LogDir           string
	WorkDir          func() (string, error)
	Out              io.Writer
	Err              io.Writer
}

// NewRootCmd constructs the root cobra command with all dcron subcommands wired to app.
func NewRootCmd(app *App) *cobra.Command {
	if app.Out == nil {
		app.Out = os.Stdout
	}
	if app.Err == nil {
		app.Err = os.Stderr
	}
	if app.WorkDir == nil {
		app.WorkDir = os.Getwd
	}

	root := &cobra.Command{
		Use:           "dcron",
		Short:         "Manage repo-local cron jobs using .cron.toml",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetOut(app.Out)
	root.SetErr(app.Err)

	root.AddCommand(
		newInitCmd(app),
		newRegisterCmd(app),
		newUnregisterCmd(app),
		newListCmd(app),
		newStatusCmd(app),
		newValidateCmd(app),
		newRunCmd(app),
		newTriggerCmd(app),
		newLogsCmd(app),
		newDaemonCmd(app),
		newTUICmd(app),
	)

	return root
}

type resolvedTarget struct {
	Name     string
	RepoPath string // canonical repo directory, or canonical global.toml file path when IsGlobal is true
	IsGlobal bool
}

func (a *App) globalConfigPath() (string, error) {
	if strings.TrimSpace(a.GlobalConfigPath) == "" {
		return "", errors.New("global config path is not configured")
	}
	canon, err := registry.CanonicalPath(a.GlobalConfigPath)
	if err != nil {
		return "", err
	}
	return canon, nil
}

func (a *App) globalConfigExists() (string, bool) {
	gp, err := a.globalConfigPath()
	if err != nil {
		return "", false
	}
	info, err := os.Stat(gp)
	if err != nil || info.IsDir() {
		return gp, false
	}
	return gp, true
}

func (a *App) resolveTarget(repoArg string, globalFlag bool) (resolvedTarget, error) {
	trimmed := strings.TrimSpace(repoArg)
	if globalFlag && trimmed != "" {
		return resolvedTarget{}, errors.New("cannot specify both --global and a repository path or name")
	}
	if globalFlag || trimmed == config.GlobalRepoName {
		gp, err := a.globalConfigPath()
		if err != nil {
			return resolvedTarget{}, err
		}
		return resolvedTarget{
			Name:     config.GlobalRepoName,
			RepoPath: gp,
			IsGlobal: true,
		}, nil
	}
	repoName, repoPath, err := a.resolveRepoPath(trimmed)
	if err != nil {
		return resolvedTarget{}, err
	}
	return resolvedTarget{
		Name:     repoName,
		RepoPath: repoPath,
		IsGlobal: false,
	}, nil
}

func (a *App) resolveJobTarget(arg, repoFlag string, globalFlag bool) (resolvedTarget, string, error) {
	if globalFlag && strings.TrimSpace(repoFlag) != "" {
		return resolvedTarget{}, "", errors.New("cannot specify both --global and --repo")
	}
	if globalFlag {
		jobName := arg
		if prefix, rest, ok := strings.Cut(arg, "/"); ok {
			if prefix != config.GlobalRepoName || rest == "" {
				return resolvedTarget{}, "", fmt.Errorf("cannot combine --global with repository target %q", arg)
			}
			jobName = rest
		}
		target, err := a.resolveTarget("", true)
		if err != nil {
			return resolvedTarget{}, "", err
		}
		return target, jobName, nil
	}

	repoRef, jobName := parseJobTarget(arg, repoFlag)
	target, err := a.resolveTarget(repoRef, false)
	if err != nil {
		return resolvedTarget{}, "", err
	}
	return target, jobName, nil
}

func (a *App) loadTargetConfig(t resolvedTarget) (*config.RepoConfig, error) {
	if t.IsGlobal {
		cfg, err := a.Loader.LoadFile(t.RepoPath, "")
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("global config %s does not exist (run `dcron init --global` to create it)", t.RepoPath)
			}
			return nil, err
		}
		return cfg, nil
	}
	return a.Loader.Load(t.RepoPath)
}

func (a *App) targetConfigPath(t resolvedTarget) string {
	if t.IsGlobal {
		return t.RepoPath
	}
	return a.Loader.ConfigPath(t.RepoPath)
}

// resolveRepoPath resolves an optional repo argument (name, path, or "." if empty).
func (a *App) resolveRepoPath(repoArg string) (repoName string, repoPath string, err error) {
	if strings.TrimSpace(repoArg) != "" {
		if entry, ok, err := a.Registry.Find(repoArg); err == nil && ok {
			return entry.Name, entry.Path, nil
		}
		canon, err := registry.CanonicalPath(repoArg)
		if err != nil {
			return "", "", err
		}
		return filepath.Base(canon), canon, nil
	}

	cwd, err := a.WorkDir()
	if err != nil {
		return "", "", fmt.Errorf("get working directory: %w", err)
	}
	canon, err := registry.CanonicalPath(cwd)
	if err != nil {
		return "", "", err
	}
	if entry, ok, err := a.Registry.Find(canon); err == nil && ok {
		return entry.Name, entry.Path, nil
	}
	return filepath.Base(canon), canon, nil
}

// parseJobTarget supports either "job" (with --repo flag or cwd) or "repo/job" shorthand.
func parseJobTarget(arg, repoFlag string) (repoRef, jobName string) {
	if repoFlag == "" {
		if r, j, ok := strings.Cut(arg, "/"); ok && r != "" && j != "" {
			return r, j
		}
	}
	return repoFlag, arg
}
