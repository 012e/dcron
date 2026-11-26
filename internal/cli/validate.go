package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/012e/dcron/internal/schedule"
)

func newValidateCmd(app *App) *cobra.Command {
	var nextCount int

	cmd := &cobra.Command{
		Use:   "validate [path]",
		Short: "Validate .cron.toml and preview upcoming run times",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repoArg := ""
			if len(args) == 1 {
				repoArg = args[0]
			}
			_, repoPath, err := app.resolveRepoPath(repoArg)
			if err != nil {
				return err
			}

			cfg, err := app.Loader.Load(repoPath)
			if err != nil {
				return err
			}

			_, _ = fmt.Fprintf(app.Out, "Valid: %s (%d job(s))\n", app.Loader.ConfigPath(cfg.RepoPath), len(cfg.Jobs))
			now := time.Now()
			for _, jn := range cfg.SortedJobNames() {
				j := cfg.Jobs[jn]
				status := "enabled"
				if !j.Enabled {
					status = "disabled"
				}
				_, _ = fmt.Fprintf(app.Out, "\n[%s] (%s)\n", j.Name, status)
				_, _ = fmt.Fprintf(app.Out, "  schedule: %s -> %s (%s)\n", j.Schedule.Raw, j.Schedule.Summary(), j.Schedule.Kind)
				_, _ = fmt.Fprintf(app.Out, "  command:  %s\n", j.Command)
				_, _ = fmt.Fprintf(app.Out, "  workdir:  %s\n", j.WorkDir)
				if j.Timeout > 0 {
					_, _ = fmt.Fprintf(app.Out, "  timeout:  %s\n", j.Timeout)
				}
				if j.Schedule.Kind == schedule.KindReboot {
					_, _ = fmt.Fprintln(app.Out, "  next:     on system boot / daemon startup")
					continue
				}
				if nextCount > 0 {
					runs, err := j.Schedule.Next(now, nextCount)
					if err != nil {
						return err
					}
					_, _ = fmt.Fprintln(app.Out, "  next runs:")
					for _, t := range runs {
						_, _ = fmt.Fprintf(app.Out, "    - %s\n", t.Local().Format(time.RFC1123))
					}
				}
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&nextCount, "next", 3, "Number of upcoming run times to preview per job")
	return cmd
}
