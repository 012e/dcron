package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// CommandExecutor abstracts external command execution (e.g., systemctl) for testability.
type CommandExecutor interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
}

// OSCommandExecutor runs commands via os/exec.
type OSCommandExecutor struct{}

func (OSCommandExecutor) Run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ServiceManager installs, uninstalls, and inspects the OS background service for dcron.
type ServiceManager interface {
	Install(ctx context.Context, binaryPath string) (string, error)
	Uninstall(ctx context.Context) error
	Status(ctx context.Context) (string, error)
	UnitPath() string
}

// SystemdManager manages a Linux systemd --user service unit for dcron.
type SystemdManager struct {
	unitDir  string
	unitName string
	exec     CommandExecutor
}

// NewSystemdManager constructs a SystemdManager writing to ~/.config/systemd/user/dcron.service
// (or XDG_CONFIG_HOME/systemd/user/dcron.service).
func NewSystemdManager(executor CommandExecutor) (*SystemdManager, error) {
	if executor == nil {
		executor = OSCommandExecutor{}
	}
	var base string
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		base = xdg
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve home directory: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	return &SystemdManager{
		unitDir:  filepath.Join(base, "systemd", "user"),
		unitName: "dcron.service",
		exec:     executor,
	}, nil
}

// NewSystemdManagerAt creates a SystemdManager targeting an explicit unit directory (useful in tests).
func NewSystemdManagerAt(unitDir string, executor CommandExecutor) *SystemdManager {
	if executor == nil {
		executor = OSCommandExecutor{}
	}
	return &SystemdManager{
		unitDir:  unitDir,
		unitName: "dcron.service",
		exec:     executor,
	}
}

func (m *SystemdManager) UnitPath() string {
	return filepath.Join(m.unitDir, m.unitName)
}

// RenderUnit renders the systemd user unit file for the given dcron binary path.
func (m *SystemdManager) RenderUnit(binaryPath string) string {
	return fmt.Sprintf(`[Unit]
Description=dcron - Repo-local cron job daemon
After=default.target

[Service]
Type=simple
ExecStart=%q daemon run
Restart=on-failure
RestartSec=3

[Install]
WantedBy=default.target
`, binaryPath)
}

// Install writes the systemd user unit file, reloads systemd, and enables+starts the service.
func (m *SystemdManager) Install(ctx context.Context, binaryPath string) (string, error) {
	absBin, err := filepath.Abs(binaryPath)
	if err != nil {
		return "", fmt.Errorf("resolve binary path: %w", err)
	}

	unitPath := m.UnitPath()
	if err := writeFileAtomic(unitPath, []byte(m.RenderUnit(absBin)), 0o644); err != nil {
		return "", fmt.Errorf("write unit file %s: %w", unitPath, err)
	}

	if out, err := m.exec.Run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		return unitPath, fmt.Errorf("systemctl daemon-reload failed: %w (%s)", err, strings.TrimSpace(out))
	}
	if out, err := m.exec.Run(ctx, "systemctl", "--user", "enable", "--now", m.unitName); err != nil {
		return unitPath, fmt.Errorf("systemctl enable --now failed: %w (%s)", err, strings.TrimSpace(out))
	}
	return unitPath, nil
}

// Uninstall disables and stops the user unit, removes the unit file, and reloads systemd.
func (m *SystemdManager) Uninstall(ctx context.Context) error {
	_, _ = m.exec.Run(ctx, "systemctl", "--user", "disable", "--now", m.unitName)
	if err := os.Remove(m.UnitPath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove unit file %s: %w", m.UnitPath(), err)
	}
	if out, err := m.exec.Run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload failed: %w (%s)", err, strings.TrimSpace(out))
	}
	return nil
}

// Status returns systemctl --user status output for dcron.service.
func (m *SystemdManager) Status(ctx context.Context) (string, error) {
	out, err := m.exec.Run(ctx, "systemctl", "--user", "status", "--no-pager", m.unitName)
	return strings.TrimSpace(out), err
}
