package daemon

import (
	"context"
	"os"
	"strings"
	"testing"
)

type recordingExecutor struct {
	calls [][]string
}

func (r *recordingExecutor) Run(_ context.Context, name string, args ...string) (string, error) {
	call := append([]string{name}, args...)
	r.calls = append(r.calls, call)
	return "active (running)", nil
}

func TestSystemdManager_InstallUninstallStatus(t *testing.T) {
	dir := t.TempDir()
	exec := &recordingExecutor{}
	mgr := NewSystemdManagerAt(dir, exec)

	unitPath, err := mgr.Install(context.Background(), "/usr/local/bin/dcron")
	if err != nil {
		t.Fatalf("Install(): %v", err)
	}

	data, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("read unit file: %v", err)
	}
	unit := string(data)
	if !strings.Contains(unit, `ExecStart="/usr/local/bin/dcron" daemon run`) {
		t.Errorf("unexpected unit file contents:\n%s", unit)
	}
	if !strings.Contains(unit, "WantedBy=default.target") {
		t.Errorf("missing WantedBy in unit:\n%s", unit)
	}

	if len(exec.calls) != 2 {
		t.Fatalf("expected 2 systemctl calls, got %d: %#v", len(exec.calls), exec.calls)
	}

	out, err := mgr.Status(context.Background())
	if err != nil || out != "active (running)" {
		t.Fatalf("Status() = (%q, %v)", out, err)
	}

	if err := mgr.Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall(): %v", err)
	}
	if _, err := os.Stat(unitPath); !os.IsNotExist(err) {
		t.Errorf("unit file should be removed after Uninstall")
	}
}
