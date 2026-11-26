package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/012e/dcron/internal/config"
)

func newInitCmd(app *App) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "init [path]",
		Short: "Create a starter .cron.toml in a repository",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			targetDir := "."
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
			_, _ = fmt.Fprintf(app.Out, "Next: edit %s, then run `dcron register` to activate it.\n", config.FileName)
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Overwrite existing .cron.toml")
	return cmd
}
