package cli

import (
	"fmt"
	"slices"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/012e/dcron/internal/config"
)

func newListCmd(app *App) *cobra.Command {
	var globalFlag bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List registered repositories, global crons, and their configured jobs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var targets []resolvedTarget

			if globalFlag {
				gp, err := app.globalConfigPath()
				if err != nil {
					return err
				}
				if _, exists := app.globalConfigExists(); !exists {
					_, _ = fmt.Fprintf(app.Out, "No global crons configured. Run `dcron init --global` to create %s.\n", gp)
					return nil
				}
				targets = append(targets, resolvedTarget{
					Name:     config.GlobalRepoName,
					RepoPath: gp,
					IsGlobal: true,
				})
			} else {
				if gp, exists := app.globalConfigExists(); exists {
					targets = append(targets, resolvedTarget{
						Name:     config.GlobalRepoName,
						RepoPath: gp,
						IsGlobal: true,
					})
				}
				entries, err := app.Registry.List()
				if err != nil {
					return err
				}
				for _, entry := range entries {
					targets = append(targets, resolvedTarget{
						Name:     entry.Name,
						RepoPath: entry.Path,
					})
				}
			}

			if len(targets) == 0 {
				_, _ = fmt.Fprintln(app.Out, "No repositories or global crons configured. Run `dcron register` in a repo or `dcron init --global`.")
				return nil
			}

			tw := tabwriter.NewWriter(app.Out, 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "REPO\tJOB\tSCHEDULE\tNORMALIZED\tENABLED\tCOMMAND")

			for _, target := range targets {
				cfg, err := app.loadTargetConfig(target)
				if err != nil {
					_, _ = fmt.Fprintf(tw, "%s\t<error>\t-\t-\t-\t%v\n", target.Name, err)
					continue
				}
				if len(cfg.Jobs) == 0 {
					_, _ = fmt.Fprintf(tw, "%s\t(no jobs)\t-\t-\t-\t-\n", target.Name)
					continue
				}
				for _, jn := range cfg.SortedJobNames() {
					j := cfg.Jobs[jn]
					_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%t\t%s\n",
						target.Name, j.Name, j.Schedule.Raw, j.Schedule.Summary(), j.Enabled, j.Command)
				}
			}
			return tw.Flush()
		},
	}

	cmd.Flags().BoolVarP(&globalFlag, "global", "g", false, "Show only user-wide global crons")
	return cmd
}

func newStatusCmd(app *App) *cobra.Command {
	var globalFlag bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show daemon health, job schedules, and last execution status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			snap, err := app.StateStore.Load()
			if err != nil {
				return err
			}

			alive := app.ProcessChecker != nil && app.ProcessChecker.IsAlive(snap.PID)
			if alive {
				uptime := time.Since(snap.StartedAt).Round(time.Second)
				_, _ = fmt.Fprintf(app.Out, "Daemon: running (pid %d, uptime %s)\n", snap.PID, uptime)
			} else {
				_, _ = fmt.Fprintln(app.Out, "Daemon: stopped (run `dcron daemon run` or `dcron daemon install`)")
			}

			var targets []resolvedTarget
			if globalFlag {
				gp, err := app.globalConfigPath()
				if err != nil {
					return err
				}
				_, fileExists := app.globalConfigExists()
				_, hasState := snap.Repos[gp]
				if !fileExists && !hasState {
					_, _ = fmt.Fprintf(app.Out, "\nNo global crons configured. Run `dcron init --global` to create %s.\n", gp)
					return nil
				}
				targets = append(targets, resolvedTarget{
					Name:     config.GlobalRepoName,
					RepoPath: gp,
					IsGlobal: true,
				})
			} else {
				if gp, fileExists := app.globalConfigExists(); gp != "" {
					if _, hasState := snap.Repos[gp]; fileExists || hasState {
						targets = append(targets, resolvedTarget{
							Name:     config.GlobalRepoName,
							RepoPath: gp,
							IsGlobal: true,
						})
					}
				}
				entries, err := app.Registry.List()
				if err != nil {
					return err
				}
				for _, entry := range entries {
					targets = append(targets, resolvedTarget{
						Name:     entry.Name,
						RepoPath: entry.Path,
					})
				}
			}

			if len(targets) == 0 {
				_, _ = fmt.Fprintln(app.Out, "\nNo repositories or global crons configured.")
				return nil
			}

			_, _ = fmt.Fprintln(app.Out)
			tw := tabwriter.NewWriter(app.Out, 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "REPO\tJOB\tSCHEDULE\tSTATE\tLAST RUN\tNEXT RUN\tRUNS")

			for _, target := range targets {
				rs, ok := snap.Repos[target.RepoPath]
				if !ok {
					_, _ = fmt.Fprintf(tw, "%s\t(not loaded)\t-\tpending\t-\t-\t0\n", target.Name)
					continue
				}
				if rs.ConfigError != "" {
					_, _ = fmt.Fprintf(tw, "%s\t<config error>\t-\terror\t-\t-\t-\n", target.Name)
					_, _ = fmt.Fprintf(app.Err, "config error in %s (%s): %s\n", target.Name, target.RepoPath, rs.ConfigError)
				}

				jobNames := make([]string, 0, len(rs.Jobs))
				for jn := range rs.Jobs {
					jobNames = append(jobNames, jn)
				}
				slices.Sort(jobNames)

				for _, jn := range jobNames {
					js := rs.Jobs[jn]
					stateStr := "idle"
					switch {
					case !js.Enabled:
						stateStr = "disabled"
					case js.Running:
						stateStr = "running"
					case js.LastError != "":
						stateStr = fmt.Sprintf("failed (exit %d)", derefInt(js.LastExitCode))
					case js.LastExitCode != nil:
						stateStr = "ok"
					}

					lastRun := "-"
					if js.LastRun != nil {
						lastRun = js.LastRun.Local().Format("2006-01-02 15:04:05")
					}
					nextRun := "-"
					if js.ScheduleKind == "reboot" {
						if js.LastBootID != "" && js.LastBootID == snap.BootID {
							nextRun = "ran this boot"
						} else if alive && js.NextRun != nil {
							nextRun = js.NextRun.Local().Format("2006-01-02 15:04:05")
						} else {
							nextRun = "on next boot"
						}
					} else if alive && js.NextRun != nil {
						nextRun = js.NextRun.Local().Format("2006-01-02 15:04:05")
					}

					_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d\n",
						target.Name, js.Name, js.ScheduleSummary, stateStr, lastRun, nextRun, js.RunCount)
				}
			}

			return tw.Flush()
		},
	}

	cmd.Flags().BoolVarP(&globalFlag, "global", "g", false, "Show status only for user-wide global crons")
	return cmd
}

func derefInt(p *int) int {
	if p == nil {
		return -1
	}
	return *p
}
