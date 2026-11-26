//go:build !windows

package runner

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func defaultShell() string {
	return "/bin/sh"
}

func shellCommandArgs(shell, command string) (string, []string) {
	if shell == "" {
		shell = defaultShell()
	}
	base := strings.ToLower(filepath.Base(shell))
	switch base {
	case "pwsh":
		return shell, []string{"-NoProfile", "-Command", command}
	default:
		return shell, []string{"-c", command}
	}
}

func buildShellCommand(ctx context.Context, shell, command string) *exec.Cmd {
	shellBin, shellArgs := shellCommandArgs(shell, command)
	return exec.CommandContext(ctx, shellBin, shellArgs...)
}

func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Kill entire process group so child scripts do not leak on timeout.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
