package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/012e/dcron/internal/runner"
)

func newLogsCmd(app *App) *cobra.Command {
	var (
		repoFlag   string
		globalFlag bool
		lines      int
		follow     bool
	)

	cmd := &cobra.Command{
		Use:   "logs <job|repo/job>",
		Short: "Display or follow execution logs for a job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target, jobName, err := app.resolveJobTarget(args[0], repoFlag, globalFlag)
			if err != nil {
				return err
			}

			logPath := runner.LogPathFor(app.LogDir, target.Name, target.RepoPath, jobName)
			f, err := os.Open(logPath)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return fmt.Errorf("no logs found for %s/%s yet (%s)", target.Name, jobName, logPath)
				}
				return fmt.Errorf("open log file %s: %w", logPath, err)
			}
			defer func() { _ = f.Close() }()

			if err := printLastLines(f, app.Out, lines); err != nil {
				return err
			}
			if !follow {
				return nil
			}
			followCtx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return followFile(followCtx, f, app.Out)
		},
	}

	cmd.Flags().StringVarP(&repoFlag, "repo", "r", "", "Repository name or path (defaults to current directory)")
	cmd.Flags().BoolVarP(&globalFlag, "global", "g", false, "Show logs for a job from user-wide global.toml")
	cmd.Flags().IntVarP(&lines, "lines", "n", 100, "Number of recent log lines to display (0 for all)")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow log output")
	return cmd
}

func printLastLines(r io.Reader, w io.Writer, n int) error {
	scanner := bufio.NewScanner(r)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	if n <= 0 {
		for scanner.Scan() {
			if _, err := fmt.Fprintln(w, scanner.Text()); err != nil {
				return err
			}
		}
		return scanner.Err()
	}

	ring := make([]string, 0, n)
	for scanner.Scan() {
		if len(ring) < n {
			ring = append(ring, scanner.Text())
		} else {
			copy(ring, ring[1:])
			ring[n-1] = scanner.Text()
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	for _, line := range ring {
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return nil
}

func followFile(ctx context.Context, f *os.File, w io.Writer) error {
	buf := make([]byte, 4096)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		n, err := f.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return werr
			}
			continue
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
