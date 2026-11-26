package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/012e/dcron/internal/config"
	"github.com/012e/dcron/internal/daemon"
	"github.com/012e/dcron/internal/registry"
	"github.com/012e/dcron/internal/runner"
	"github.com/012e/dcron/internal/schedule"
	"github.com/012e/dcron/internal/state"
)

type fakeServiceManager struct {
	installed bool
	unitPath  string
}

func (f *fakeServiceManager) Install(_ context.Context, _ string) (string, error) {
	f.installed = true
	return f.unitPath, nil
}
func (f *fakeServiceManager) Uninstall(_ context.Context) error {
	f.installed = false
	return nil
}
func (f *fakeServiceManager) Status(_ context.Context) (string, error) {
	if f.installed {
		return "active (running)", nil
	}
	return "inactive (dead)", nil
}
func (f *fakeServiceManager) UnitPath() string { return f.unitPath }

type fakeProcessChecker bool

func (f fakeProcessChecker) IsAlive(int) bool { return bool(f) }

func newTestApp(t *testing.T, workDir string) (*App, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	tmp := t.TempDir()
	out := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}
	parser := schedule.NewHybridParser()

	app := &App{
		Registry:       registry.NewFileStore(filepath.Join(tmp, "registry.toml")),
		Loader:         config.NewTOMLLoader(parser),
		Parser:         parser,
		Runner:         runner.NewExecRunner(),
		StateStore:     state.NewFileStore(filepath.Join(tmp, "state.json")),
		ProcessChecker: fakeProcessChecker(false),
		ServiceManager: &fakeServiceManager{unitPath: filepath.Join(tmp, "dcron.service")},
		LogDir:         filepath.Join(tmp, "logs"),
		WorkDir:        func() (string, error) { return workDir, nil },
		Out:            out,
		Err:            errBuf,
	}
	return app, out, errBuf
}

func runCLI(t *testing.T, app *App, out, errBuf *bytes.Buffer, args ...string) error {
	t.Helper()
	out.Reset()
	errBuf.Reset()
	cmd := NewRootCmd(app)
	cmd.SetArgs(args)
	return cmd.Execute()
}

func TestCLI_EndToEndWorkflow(t *testing.T) {
	repoDir := filepath.Join(t.TempDir(), "demo-repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	app, out, errBuf := newTestApp(t, repoDir)

	// 1. init
	if err := runCLI(t, app, out, errBuf, "init"); err != nil {
		t.Fatalf("init failed: %v", err)
	}
	if !strings.Contains(out.String(), "Created") {
		t.Errorf("unexpected init output: %s", out.String())
	}

	// Overwrite with custom jobs for testing.
	customCfg := `
[jobs.greet]
description = "Say hello"
schedule = "every weekday at 9am"
command = "echo hello-from-dcron"

[jobs.boot]
schedule = "on reboot"
command = "echo booted"
`
	if err := os.WriteFile(filepath.Join(repoDir, config.FileName), []byte(customCfg), 0o644); err != nil {
		t.Fatal(err)
	}

	// 2. validate
	if err := runCLI(t, app, out, errBuf, "validate", "--next", "2"); err != nil {
		t.Fatalf("validate failed: %v", err)
	}
	if !strings.Contains(out.String(), "0 9 * * 1-5") || !strings.Contains(out.String(), "@reboot") {
		t.Errorf("unexpected validate output:\n%s", out.String())
	}

	// 3. register
	if err := runCLI(t, app, out, errBuf, "register"); err != nil {
		t.Fatalf("register failed: %v", err)
	}
	if !strings.Contains(out.String(), `Registered "demo-repo"`) {
		t.Errorf("unexpected register output: %s", out.String())
	}

	// 4. list
	if err := runCLI(t, app, out, errBuf, "list"); err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if !strings.Contains(out.String(), "greet") || !strings.Contains(out.String(), "0 9 * * 1-5") {
		t.Errorf("unexpected list output:\n%s", out.String())
	}

	// 5. run (using repo/job shorthand)
	if err := runCLI(t, app, out, errBuf, "run", "demo-repo/greet"); err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if !strings.Contains(out.String(), "hello-from-dcron") {
		t.Errorf("unexpected run stdout: %q", out.String())
	}

	// 6. logs
	if err := runCLI(t, app, out, errBuf, "logs", "greet"); err != nil {
		t.Fatalf("logs failed: %v", err)
	}
	if !strings.Contains(out.String(), "hello-from-dcron") || !strings.Contains(out.String(), "END exit=0") {
		t.Errorf("unexpected logs output:\n%s", out.String())
	}

	// 7. status with persisted state
	now := time.Now().UTC()
	code := 0
	if err := app.StateStore.Save(state.DaemonSnapshot{
		PID:       9999,
		StartedAt: now.Add(-time.Minute),
		UpdatedAt: now,
		BootID:    "b1",
		Repos: map[string]state.RepoState{
			repoDir: {
				Name: "demo-repo",
				Path: repoDir,
				Jobs: map[string]state.JobState{
					"greet": {
						Name:            "greet",
						ScheduleSummary: "0 9 * * 1-5",
						ScheduleKind:    "cron",
						Enabled:         true,
						LastRun:         &now,
						LastExitCode:    &code,
						RunCount:        1,
					},
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	app.ProcessChecker = fakeProcessChecker(true)
	if err := runCLI(t, app, out, errBuf, "status"); err != nil {
		t.Fatalf("status failed: %v", err)
	}
	if !strings.Contains(out.String(), "Daemon: running") || !strings.Contains(out.String(), "ok") {
		t.Errorf("unexpected status output:\n%s", out.String())
	}

	// 8. daemon install/status/uninstall
	if err := runCLI(t, app, out, errBuf, "daemon", "install"); err != nil {
		t.Fatalf("daemon install failed: %v", err)
	}
	if err := runCLI(t, app, out, errBuf, "daemon", "status"); err != nil {
		t.Fatalf("daemon status failed: %v", err)
	}
	if !strings.Contains(out.String(), "active (running)") {
		t.Errorf("unexpected daemon status: %s", out.String())
	}
	if err := runCLI(t, app, out, errBuf, "daemon", "uninstall"); err != nil {
		t.Fatalf("daemon uninstall failed: %v", err)
	}

	// 9. unregister
	if err := runCLI(t, app, out, errBuf, "unregister", "demo-repo"); err != nil {
		t.Fatalf("unregister failed: %v", err)
	}
	if !strings.Contains(out.String(), `Unregistered "demo-repo"`) {
		t.Errorf("unexpected unregister output: %s", out.String())
	}
}

var _ daemon.ServiceManager = (*fakeServiceManager)(nil)
