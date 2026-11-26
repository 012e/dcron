package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/012e/dcron/internal/config"
	"github.com/012e/dcron/internal/registry"
)

func newInitCmd(app *App) *cobra.Command {
	var (
		force bool
		name  string
	)

	cmd := &cobra.Command{
		Use:   "init [path]",
		Short: "Create a starter .cron.toml in a repository and register it",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var targetDir string
			if len(args) == 1 {
				targetDir = args[0]
			} else {
				cwd, err := app.WorkDir()
				if err != nil {
					return err
				}
				targetDir = cwd
			}

			absDir, err := filepath.Abs(targetDir)
			if err != nil {
				return fmt.Errorf("resolve directory: %w", err)
			}
			cfgPath := app.Loader.ConfigPath(absDir)
			if !force {
				if _, err := os.Stat(cfgPath); err == nil {
					return fmt.Errorf("%s already exists (use --force to overwrite)", cfgPath)
				} else if !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}

			if err := os.WriteFile(cfgPath, []byte(config.StarterTemplate()), 0o644); err != nil {
				return fmt.Errorf("write %s: %w", cfgPath, err)
			}

			_, _ = fmt.Fprintf(app.Out, "Created %s\n", cfgPath)

			if app.Registry != nil {
				entry, err := app.Registry.Add(absDir, name)
				if err != nil {
					if errors.Is(err, registry.ErrAlreadyRegistered) {
						if existing, ok, findErr := app.Registry.Find(absDir); findErr == nil && ok {
							_, _ = fmt.Fprintf(app.Out, "Repository already registered as %q (%s).\n", existing.Name, existing.Path)
						}
					} else {
						return fmt.Errorf("register %s: %w", absDir, err)
					}
				} else {
					_, _ = fmt.Fprintf(app.Out, "Registered %q (%s).\n", entry.Name, entry.Path)
				}
			}

			_, _ = fmt.Fprintf(app.Out, "Next: edit %s, then run `dcron validate` or `dcron tui`.\n", config.FileName)
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Overwrite existing .cron.toml")
	cmd.Flags().StringVarP(&name, "name", "n", "", "Custom alias for the repository (defaults to directory name)")
	return cmd
}
