package cli

import (
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/012e/dcron/internal/config"
	"github.com/012e/dcron/internal/daemon"
	"github.com/012e/dcron/internal/runner"
	"github.com/012e/dcron/internal/state"
)

func newTriggerCmd(app *App) *cobra.Command {
	var (
		repoFlag  string
		waitFlag  bool
		localFlag bool
	)

	cmd := &cobra.Command{
		Use:   "trigger <job|repo/job>",
		Short: "Manually trigger a job (via the running daemon, or locally if stopped)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repoRef, jobName := parseJobTarget(args[0], repoFlag)
			repoName, repoPath, err := app.resolveRepoPath(repoRef)
			if err != nil {
				return err
			}

			if !localFlag && app.Controller != nil {
				resp, err := app.Controller.Trigger(cmd.Context(), daemon.TriggerRequest{
					RepoPath: repoPath,
					JobName:  jobName,
					Wait:     waitFlag,
				})
				if err == nil {
					displayRepo := resp.RepoName
					if displayRepo == "" {
						displayRepo = repoName
					}
					if waitFlag {
						dur := (time.Duration(resp.DurationMs) * time.Millisecond).Round(time.Millisecond)
						if resp.Error != "" {
							return fmt.Errorf("job %s/%s failed in daemon (exit %d, %s): %s",
								displayRepo, jobName, resp.ExitCode, dur, resp.Error)
						}
						_, _ = fmt.Fprintf(app.Out, "Triggered and completed %s/%s in daemon (%s, exit %d)\n",
							displayRepo, jobName, dur, resp.ExitCode)
						return nil
					}
					_, _ = fmt.Fprintf(app.Out, "Triggered %s/%s in daemon (follow logs with `dcron logs %s/%s -f`)\n",
						displayRepo, jobName, displayRepo, jobName)
					return nil
				}
				if !errors.Is(err, daemon.ErrDaemonNotRunning) {
					return err
				}
				_, _ = fmt.Fprintf(app.Err, "Daemon is not running; executing %s/%s locally...\n", repoName, jobName)
			}

			return runAndRecordLocally(cmd, app, repoName, repoPath, jobName)
		},
	}

	cmd.Flags().StringVarP(&repoFlag, "repo", "r", "", "Repository name or path (defaults to current directory)")
	cmd.Flags().BoolVarP(&waitFlag, "wait", "w", false, "Wait for the daemon-triggered job to finish")
	cmd.Flags().BoolVar(&localFlag, "local", false, "Execute locally instead of sending to the running daemon")
	return cmd
}

func runAndRecordLocally(cmd *cobra.Command, app *App, repoName, repoPath, jobName string) error {
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

	res := app.Runner.Run(cmd.Context(), runner.Request{
		RepoName: repoName,
		RepoPath: repoPath,
		Job:      job,
		LogPath:  logPath,
		Stdout:   app.Out,
		Stderr:   app.Err,
	})

	recordLocalRunState(app.StateStore, app.ProcessChecker, repoName, repoPath, job, logPath, res)

	if res.Err != nil {
		return fmt.Errorf("job %s/%s failed (exit %d, %s): %w",
			repoName, job.Name, res.ExitCode, res.Duration.Round(time.Millisecond), res.Err)
	}
	_, _ = fmt.Fprintf(app.Err, "Completed %s/%s in %s\n", repoName, job.Name, res.Duration.Round(time.Millisecond))
	return nil
}

func recordLocalRunState(store state.Store, checker state.ProcessChecker, repoName, repoPath string, job config.Job, logPath string, res runner.Result) {
	if store == nil {
		return
	}
	snap, err := store.Load()
	if err != nil {
		return
	}
	// Do not overwrite state owned by a live daemon process.
	if checker != nil && snap.PID > 0 && checker.IsAlive(snap.PID) {
		return
	}
	if snap.Repos == nil {
		snap.Repos = make(map[string]state.RepoState)
	}
	rs, ok := snap.Repos[repoPath]
	if !ok {
		rs = state.RepoState{
			Name: repoName,
			Path: repoPath,
			Jobs: make(map[string]state.JobState),
		}
	}
	if rs.Jobs == nil {
		rs.Jobs = make(map[string]state.JobState)
	}
	js := rs.Jobs[job.Name]
	js.Name = job.Name
	js.Description = job.Description
	js.ScheduleRaw = job.Schedule.Raw
	js.ScheduleSummary = job.Schedule.Summary()
	js.ScheduleKind = job.Schedule.Kind.String()
	js.Enabled = job.Enabled
	js.Running = false
	started := res.StartedAt
	js.LastRun = &started
	js.LastDurationMs = res.Duration.Milliseconds()
	exitCode := res.ExitCode
	js.LastExitCode = &exitCode
	if res.Err != nil {
		js.LastError = res.Err.Error()
	} else {
		js.LastError = ""
	}
	js.RunCount++
	if logPath != "" {
		js.LogPath = logPath
	}

	rs.Name = repoName
	rs.Path = repoPath
	rs.Jobs[job.Name] = js
	snap.Repos[repoPath] = rs
	snap.UpdatedAt = time.Now().UTC()
	_ = store.Save(snap)
}
