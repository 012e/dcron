package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newRegisterCmd(app *App) *cobra.Command {
	var name string

	cmd := &cobra.Command{
		Use:   "register [path]",
		Short: "Register a repository directory with dcron",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := ""
			if len(args) == 1 {
				target = args[0]
			} else {
				cwd, err := app.WorkDir()
				if err != nil {
					return err
				}
				target = cwd
			}

			cfg, err := app.Loader.Load(target)
			if err != nil {
				return fmt.Errorf("cannot register %s: %w", target, err)
			}

			entry, err := app.Registry.Add(cfg.RepoPath, name)
			if err != nil {
				return err
			}

			_, _ = fmt.Fprintf(app.Out, "Registered %q (%s) with %d job(s).\n", entry.Name, entry.Path, len(cfg.Jobs))
			return nil
		},
	}

	cmd.Flags().StringVarP(&name, "name", "n", "", "Custom alias for the repository (defaults to directory name)")
	return cmd
}

func newUnregisterCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "unregister [path-or-name]",
		Short: "Unregister a repository from dcron",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := ""
			if len(args) == 1 {
				target = args[0]
			} else {
				cwd, err := app.WorkDir()
				if err != nil {
					return err
				}
				target = cwd
			}

			removed, err := app.Registry.Remove(target)
			if err != nil {
				return err
			}

			_, _ = fmt.Fprintf(app.Out, "Unregistered %q (%s).\n", removed.Name, removed.Path)
			return nil
		},
	}
}
