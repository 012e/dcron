package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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

	binPath := filepath.Join(dir, "dcron")
	absBin, err := filepath.Abs(binPath)
	if err != nil {
		t.Fatal(err)
	}
	unitPath, err := mgr.Install(context.Background(), binPath)
	if err != nil {
		t.Fatalf("Install(): %v", err)
	}

	data, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("read unit file: %v", err)
	}
	unit := string(data)
	wantExec := fmt.Sprintf("ExecStart=%q daemon run", absBin)
	if !strings.Contains(unit, wantExec) {
		t.Errorf("unexpected unit file contents (want %q):\n%s", wantExec, unit)
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
