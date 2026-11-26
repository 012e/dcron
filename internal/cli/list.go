package cli

import (
	"fmt"
	"slices"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

func newListCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List registered repositories and their configured jobs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			entries, err := app.Registry.List()
			if err != nil {
				return err
			}
			if len(entries) == 0 {
				_, _ = fmt.Fprintln(app.Out, "No repositories registered. Run `dcron register` inside a repo with a .cron.toml file.")
				return nil
			}

			tw := tabwriter.NewWriter(app.Out, 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "REPO\tJOB\tSCHEDULE\tNORMALIZED\tENABLED\tCOMMAND")

			for _, entry := range entries {
				cfg, err := app.Loader.Load(entry.Path)
				if err != nil {
					_, _ = fmt.Fprintf(tw, "%s\t<error>\t-\t-\t-\t%v\n", entry.Name, err)
					continue
				}
				if len(cfg.Jobs) == 0 {
					_, _ = fmt.Fprintf(tw, "%s\t(no jobs)\t-\t-\t-\t-\n", entry.Name)
					continue
				}
				for _, jn := range cfg.SortedJobNames() {
					j := cfg.Jobs[jn]
					_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%t\t%s\n",
						entry.Name, j.Name, j.Schedule.Raw, j.Schedule.Summary(), j.Enabled, j.Command)
				}
			}
			return tw.Flush()
		},
	}
}

func newStatusCmd(app *App) *cobra.Command {
	return &cobra.Command{
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

			entries, err := app.Registry.List()
			if err != nil {
				return err
			}
			if len(entries) == 0 {
				_, _ = fmt.Fprintln(app.Out, "\nNo repositories registered.")
				return nil
			}

			_, _ = fmt.Fprintln(app.Out)
			tw := tabwriter.NewWriter(app.Out, 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "REPO\tJOB\tSCHEDULE\tSTATE\tLAST RUN\tNEXT RUN\tRUNS")

			for _, entry := range entries {
				rs, ok := snap.Repos[entry.Path]
				if !ok {
					_, _ = fmt.Fprintf(tw, "%s\t(not loaded)\t-\tpending\t-\t-\t0\n", entry.Name)
					continue
				}
				if rs.ConfigError != "" {
					_, _ = fmt.Fprintf(tw, "%s\t<config error>\t-\terror\t-\t-\t-\n", entry.Name)
					_, _ = fmt.Fprintf(app.Err, "config error in %s (%s): %s\n", entry.Name, entry.Path, rs.ConfigError)
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
						} else {
							nextRun = "on next boot"
						}
					} else if alive && js.NextRun != nil {
						nextRun = js.NextRun.Local().Format("2006-01-02 15:04:05")
					}

					_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d\n",
						entry.Name, js.Name, js.ScheduleSummary, stateStr, lastRun, nextRun, js.RunCount)
				}
			}

			return tw.Flush()
		},
	}
}

func derefInt(p *int) int {
	if p == nil {
		return -1
	}
	return *p
}
