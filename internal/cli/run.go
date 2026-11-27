package cli

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/012e/dcron/internal/runner"
)

func newRunCmd(app *App) *cobra.Command {
	var (
		repoFlag   string
		globalFlag bool
	)

	cmd := &cobra.Command{
		Use:   "run <job|repo/job>",
		Short: "Run a configured job immediately in the foreground",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target, jobName, err := app.resolveJobTarget(args[0], repoFlag, globalFlag)
			if err != nil {
				return err
			}

			cfg, err := app.loadTargetConfig(target)
			if err != nil {
				return err
			}
			job, ok := cfg.Jobs[jobName]
			if !ok {
				return fmt.Errorf("job %q not found in %s (available: %v)", jobName, app.targetConfigPath(target), cfg.SortedJobNames())
			}

			logPath := ""
			if app.LogDir != "" {
				logPath = runner.LogPathFor(app.LogDir, target.Name, target.RepoPath, job.Name)
			}

			runCtx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			_, _ = fmt.Fprintf(app.Err, "Running %s/%s: %s\n", target.Name, job.Name, job.Command)
			res := app.Runner.Run(runCtx, runner.Request{
				RepoName: target.Name,
				RepoPath: target.RepoPath,
				Job:      job,
				LogPath:  logPath,
				Stdout:   app.Out,
				Stderr:   app.Err,
			})
			if res.Err != nil {
				return fmt.Errorf("job %s/%s failed (exit %d, %s): %w",
					target.Name, job.Name, res.ExitCode, res.Duration.Round(time.Millisecond), res.Err)
			}
			_, _ = fmt.Fprintf(app.Err, "Completed %s/%s in %s\n", target.Name, job.Name, res.Duration.Round(time.Millisecond))
			return nil
		},
	}

	cmd.Flags().StringVarP(&repoFlag, "repo", "r", "", "Repository name or path (defaults to current directory)")
	cmd.Flags().BoolVarP(&globalFlag, "global", "g", false, "Run a job from user-wide global.toml")
	return cmd
}
