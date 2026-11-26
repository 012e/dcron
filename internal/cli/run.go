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
	var repoFlag string

	cmd := &cobra.Command{
		Use:   "run <job|repo/job>",
		Short: "Run a configured job immediately in the foreground",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repoRef, jobName := parseJobTarget(args[0], repoFlag)
			repoName, repoPath, err := app.resolveRepoPath(repoRef)
			if err != nil {
				return err
			}

			cfg, err := app.Loader.Load(repoPath)
			if err != nil {
				return err
			}
			job, ok := cfg.Jobs[jobName]
			if !ok {
				return fmt.Errorf("job %q not found in %s (available: %v)", jobName, app.Loader.ConfigPath(repoPath), cfg.SortedJobNames())
			}

			logPath := ""
			if app.LogDir != "" {
				logPath = runner.LogPathFor(app.LogDir, repoName, repoPath, job.Name)
			}

			runCtx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			_, _ = fmt.Fprintf(app.Err, "Running %s/%s: %s\n", repoName, job.Name, job.Command)
			res := app.Runner.Run(runCtx, runner.Request{
				RepoName: repoName,
				RepoPath: repoPath,
				Job:      job,
				LogPath:  logPath,
				Stdout:   app.Out,
				Stderr:   app.Err,
			})
			if res.Err != nil {
				return fmt.Errorf("job %s/%s failed (exit %d, %s): %w",
					repoName, job.Name, res.ExitCode, res.Duration.Round(time.Millisecond), res.Err)
			}
			_, _ = fmt.Fprintf(app.Err, "Completed %s/%s in %s\n", repoName, job.Name, res.Duration.Round(time.Millisecond))
			return nil
		},
	}

	cmd.Flags().StringVarP(&repoFlag, "repo", "r", "", "Repository name or path (defaults to current directory)")
	return cmd
}
