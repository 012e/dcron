//go:build windows

package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func defaultShell() string {
	if comspec := os.Getenv("COMSPEC"); comspec != "" {
		return comspec
	}
	return "cmd.exe"
}

func isCmdShell(shell string) bool {
	base := strings.ToLower(filepath.Base(shell))
	return base == "cmd" || base == "cmd.exe"
}

func shellCommandArgs(shell, command string) (string, []string) {
	if shell == "" {
		shell = defaultShell()
	}
	base := strings.ToLower(filepath.Base(shell))
	switch base {
	case "cmd", "cmd.exe":
		return shell, []string{"/S", "/C", command}
	case "powershell", "powershell.exe", "pwsh", "pwsh.exe":
		return shell, []string{"-NoProfile", "-Command", command}
	default:
		return shell, []string{"-c", command}
	}
}

func buildShellCommand(ctx context.Context, shell, command string) *exec.Cmd {
	if shell == "" {
		shell = defaultShell()
	}
	if isCmdShell(shell) {
		// Pass /S /C "<command>" via raw CmdLine so Go's EscapeArg does not
		// backslash-escape internal quotes that cmd.exe treats literally.
		cmd := exec.CommandContext(ctx, shell)
		cmd.SysProcAttr = &syscall.SysProcAttr{
			HideWindow: true,
			CmdLine:    syscall.EscapeArg(shell) + ` /S /C "` + command + `"`,
		}
		return cmd
	}
	shellBin, shellArgs := shellCommandArgs(shell, command)
	cmd := exec.CommandContext(ctx, shellBin, shellArgs...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}

func configureProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Terminate the process tree so child processes do not leak on timeout.
		killCmd := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
		killCmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if err := killCmd.Run(); err == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
}
