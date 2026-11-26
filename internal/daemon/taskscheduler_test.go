package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsTaskManager_InstallUninstallStatus(t *testing.T) {
	dir := t.TempDir()
	xmlPath := filepath.Join(dir, "state", "dcron-task.xml")
	exec := &recordingExecutor{}
	mgr := NewWindowsTaskManagerAt(xmlPath, exec)

	// Path with a space to verify quoting and XML escaping.
	binPath := filepath.Join(dir, "Program Files", "dcron.exe")
	absBin, err := filepath.Abs(binPath)
	if err != nil {
		t.Fatal(err)
	}

	gotPath, err := mgr.Install(context.Background(), binPath)
	if err != nil {
		t.Fatalf("Install(): %v", err)
	}
	if gotPath != xmlPath {
		t.Errorf("Install() path = %q, want %q", gotPath, xmlPath)
	}

	data, err := os.ReadFile(xmlPath)
	if err != nil {
		t.Fatalf("read task xml: %v", err)
	}
	xmlText := string(data)
	wantCmd := `<Command>` + xmlEscape(`"`+absBin+`"`) + `</Command>`
	for _, want := range []string{
		wantCmd,
		"<Arguments>daemon run</Arguments>",
		"<LogonTrigger>",
		"<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>",
		"<DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>",
		"<RunLevel>LeastPrivilege</RunLevel>",
		"<Hidden>true</Hidden>",
	} {
		if !strings.Contains(xmlText, want) {
			t.Errorf("task XML missing %q:\n%s", want, xmlText)
		}
	}

	if len(exec.calls) != 2 {
		t.Fatalf("expected 2 schtasks calls, got %d: %#v", len(exec.calls), exec.calls)
	}
	if exec.calls[0][0] != "schtasks" || exec.calls[0][1] != "/Create" {
		t.Errorf("unexpected first schtasks call: %#v", exec.calls[0])
	}
	if strings.Join(exec.calls[1], " ") != "schtasks /Run /TN dcron" {
		t.Errorf("unexpected second schtasks call: %#v", exec.calls[1])
	}

	out, err := mgr.Status(context.Background())
	if err != nil || out != "active (running)" {
		t.Fatalf("Status() = (%q, %v)", out, err)
	}
	if last := exec.calls[len(exec.calls)-1]; strings.Join(last, " ") != "schtasks /Query /TN dcron /FO LIST /V" {
		t.Errorf("unexpected status call: %#v", last)
	}

	if err := mgr.Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall(): %v", err)
	}
	if _, err := os.Stat(xmlPath); !os.IsNotExist(err) {
		t.Errorf("task xml should be removed after Uninstall")
	}
}
