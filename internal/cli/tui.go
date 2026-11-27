package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/012e/dcron/internal/tui"
)

func newTUICmd(app *App) *cobra.Command {
	var (
		repoFlag   string
		globalFlag bool
		refresh    time.Duration
	)

	cmd := &cobra.Command{
		Use:     "tui",
		Aliases: []string{"ui"},
		Short:   "Launch the interactive terminal dashboard for managing repos, jobs, and logs",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if refresh <= 0 {
				return fmt.Errorf("--refresh must be positive (got %s)", refresh)
			}

			initialRepo := ""
			if globalFlag || repoFlag != "" {
				target, err := app.resolveTarget(repoFlag, globalFlag)
				if err != nil {
					return err
				}
				initialRepo = target.RepoPath
			}

			deps := tui.Deps{
				Registry:         app.Registry,
				Loader:           app.Loader,
				Parser:           app.Parser,
				Runner:           app.Runner,
				StateStore:       app.StateStore,
				ProcessChecker:   app.ProcessChecker,
				ServiceManager:   app.ServiceManager,
				Controller:       app.Controller,
				GlobalConfigPath: app.GlobalConfigPath,
				LogDir:           app.LogDir,
				WorkDir:          app.WorkDir,
				RefreshInterval:  refresh,
				InitialRepo:      initialRepo,
			}

			runFn := app.TUIRunner
			if runFn == nil {
				runFn = tui.Run
			}
			return runFn(cmd.Context(), deps)
		},
	}

	cmd.Flags().StringVarP(&repoFlag, "repo", "r", "", "Pre-select a repository by name or path")
	cmd.Flags().BoolVarP(&globalFlag, "global", "g", false, "Pre-select user-wide global crons")
	cmd.Flags().DurationVar(&refresh, "refresh", 2*time.Second, "Auto-refresh interval for daemon state and logs")
	return cmd
}
