package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaunchdManager_InstallUninstallStatus(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "logs", "daemon.log")
	exec := &recordingExecutor{}
	mgr := NewLaunchdManagerAt(dir, logPath, 501, exec)

	binPath := filepath.Join(dir, "bin", "dcron")
	absBin, err := filepath.Abs(binPath)
	if err != nil {
		t.Fatal(err)
	}

	plistPath, err := mgr.Install(context.Background(), binPath)
	if err != nil {
		t.Fatalf("Install(): %v", err)
	}
	if plistPath != mgr.UnitPath() {
		t.Errorf("plistPath = %q, want %q", plistPath, mgr.UnitPath())
	}

	data, err := os.ReadFile(plistPath)
	if err != nil {
		t.Fatalf("read plist: %v", err)
	}
	plist := string(data)
	for _, want := range []string{
		"<string>com.012e.dcron</string>",
		"<string>" + xmlEscape(absBin) + "</string>",
		"<string>daemon</string>",
		"<string>run</string>",
		"<key>RunAtLoad</key>",
		"<key>SuccessfulExit</key>",
		"<string>" + xmlEscape(logPath) + "</string>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist missing %q:\n%s", want, plist)
		}
	}

	// Expect bootout (best-effort) + bootstrap.
	if len(exec.calls) != 2 {
		t.Fatalf("expected 2 launchctl calls, got %d: %#v", len(exec.calls), exec.calls)
	}
	if strings.Join(exec.calls[1], " ") != "launchctl bootstrap gui/501 "+plistPath {
		t.Errorf("unexpected bootstrap call: %#v", exec.calls[1])
	}

	out, err := mgr.Status(context.Background())
	if err != nil || out != "active (running)" {
		t.Fatalf("Status() = (%q, %v)", out, err)
	}
	if last := exec.calls[len(exec.calls)-1]; strings.Join(last, " ") != "launchctl print gui/501/com.012e.dcron" {
		t.Errorf("unexpected status call: %#v", last)
	}

	if err := mgr.Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall(): %v", err)
	}
	if _, err := os.Stat(plistPath); !os.IsNotExist(err) {
		t.Errorf("plist should be removed after Uninstall")
	}
}
