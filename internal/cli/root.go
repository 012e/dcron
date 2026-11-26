package cli

import (
	"context"
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
	Registry       registry.Store
	Loader         config.Loader
	Parser         schedule.Parser
	Runner         runner.Runner
	StateStore     state.Store
	ProcessChecker state.ProcessChecker
	ServiceManager daemon.ServiceManager
	Controller     daemon.Controller
	DaemonFactory  func() (daemon.Daemon, error)
	TUIRunner      func(ctx context.Context, deps tui.Deps) error
	LogDir         string
	WorkDir        func() (string, error)
	Out            io.Writer
	Err            io.Writer
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
