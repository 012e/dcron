package cli

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

func newDaemonCmd(app *App) *cobra.Command {
	daemonCmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run or manage the background dcron daemon",
	}

	runCmd := &cobra.Command{
		Use:   "run",
		Short: "Run the dcron scheduler daemon in the foreground",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.DaemonFactory == nil {
				return fmt.Errorf("daemon factory not configured")
			}
			d, err := app.DaemonFactory()
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return d.Run(ctx)
		},
	}

	installCmd := &cobra.Command{
		Use:   "install",
		Short: "Install and start dcron as a user background service (systemd, launchd, or Task Scheduler)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ServiceManager == nil {
				return fmt.Errorf("service manager unavailable on this platform")
			}
			bin, err := os.Executable()
			if err != nil {
				return fmt.Errorf("locate dcron binary: %w", err)
			}
			unitPath, err := app.ServiceManager.Install(cmd.Context(), bin)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(app.Out, "Installed and started dcron background service (%s)\n", unitPath)
			return nil
		},
	}

	uninstallCmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Stop and remove the dcron user background service",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ServiceManager == nil {
				return fmt.Errorf("service manager unavailable on this platform")
			}
			if err := app.ServiceManager.Uninstall(cmd.Context()); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(app.Out, "Uninstalled dcron background service.")
			return nil
		},
	}

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show OS background service status for dcron",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.ServiceManager == nil {
				return fmt.Errorf("service manager unavailable on this platform")
			}
			out, err := app.ServiceManager.Status(cmd.Context())
			if out != "" {
				_, _ = fmt.Fprintln(app.Out, out)
			}
			return err
		},
	}

	daemonCmd.AddCommand(runCmd, installCmd, uninstallCmd, statusCmd)
	return daemonCmd
}
