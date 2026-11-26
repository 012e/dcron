package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/012e/dcron/internal/state"
)

const windowsTaskName = "dcron"

// WindowsTaskManager manages a Windows Task Scheduler user task for dcron.
type WindowsTaskManager struct {
	xmlPath  string
	taskName string
	exec     CommandExecutor
}

// NewWindowsTaskManager constructs a WindowsTaskManager storing its task definition XML
// inside dcron's state directory.
func NewWindowsTaskManager(executor CommandExecutor) (*WindowsTaskManager, error) {
	if executor == nil {
		executor = OSCommandExecutor{}
	}
	stateDir, err := state.DefaultStateDir()
	if err != nil {
		return nil, fmt.Errorf("resolve state directory: %w", err)
	}
	return &WindowsTaskManager{
		xmlPath:  filepath.Join(stateDir, "dcron-task.xml"),
		taskName: windowsTaskName,
		exec:     executor,
	}, nil
}

// NewWindowsTaskManagerAt creates a WindowsTaskManager writing its XML to xmlPath (useful in tests).
func NewWindowsTaskManagerAt(xmlPath string, executor CommandExecutor) *WindowsTaskManager {
	if executor == nil {
		executor = OSCommandExecutor{}
	}
	return &WindowsTaskManager{
		xmlPath:  xmlPath,
		taskName: windowsTaskName,
		exec:     executor,
	}
}

func (m *WindowsTaskManager) UnitPath() string {
	return m.xmlPath
}

// RenderTaskXML renders the Windows Task Scheduler XML 1.2 definition for binaryPath.
func (m *WindowsTaskManager) RenderTaskXML(binaryPath string) string {
	cmdPath := binaryPath
	if strings.ContainsAny(cmdPath, " \t") && (!strings.HasPrefix(cmdPath, `"`) || !strings.HasSuffix(cmdPath, `"`)) {
		cmdPath = `"` + cmdPath + `"`
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>dcron - Repo-local cron job daemon</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>3</Count>
    </RestartOnFailure>
    <Enabled>true</Enabled>
    <Hidden>true</Hidden>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>%s</Command>
      <Arguments>daemon run</Arguments>
    </Exec>
  </Actions>
</Task>
`, xmlEscape(cmdPath))
}

// Install writes the Task Scheduler XML definition, registers the task via schtasks, and starts it.
func (m *WindowsTaskManager) Install(ctx context.Context, binaryPath string) (string, error) {
	absBin, err := filepath.Abs(binaryPath)
	if err != nil {
		return "", fmt.Errorf("resolve binary path: %w", err)
	}
	xmlPath := m.UnitPath()
	if err := writeFileAtomic(xmlPath, []byte(m.RenderTaskXML(absBin)), 0o644); err != nil {
		return "", fmt.Errorf("write task definition %s: %w", xmlPath, err)
	}

	if out, err := m.exec.Run(ctx, "schtasks", "/Create", "/TN", m.taskName, "/XML", xmlPath, "/F"); err != nil {
		return xmlPath, fmt.Errorf("schtasks /Create failed: %w (%s)", err, strings.TrimSpace(out))
	}
	if out, err := m.exec.Run(ctx, "schtasks", "/Run", "/TN", m.taskName); err != nil {
		return xmlPath, fmt.Errorf("schtasks /Run failed: %w (%s)", err, strings.TrimSpace(out))
	}
	return xmlPath, nil
}

// Uninstall ends and deletes the scheduled task and removes the stored XML file.
func (m *WindowsTaskManager) Uninstall(ctx context.Context) error {
	_, _ = m.exec.Run(ctx, "schtasks", "/End", "/TN", m.taskName)
	if out, err := m.exec.Run(ctx, "schtasks", "/Delete", "/TN", m.taskName, "/F"); err != nil {
		// Ignore missing-task errors so Uninstall is idempotent.
		lower := strings.ToLower(out)
		if !strings.Contains(lower, "cannot find") && !strings.Contains(lower, "does not exist") {
			return fmt.Errorf("schtasks /Delete failed: %w (%s)", err, strings.TrimSpace(out))
		}
	}
	if err := os.Remove(m.UnitPath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove task definition %s: %w", m.UnitPath(), err)
	}
	return nil
}

// Status returns schtasks query output for the dcron scheduled task.
func (m *WindowsTaskManager) Status(ctx context.Context) (string, error) {
	out, err := m.exec.Run(ctx, "schtasks", "/Query", "/TN", m.taskName, "/FO", "LIST", "/V")
	return strings.TrimSpace(out), err
}
