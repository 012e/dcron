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
	"github.com/012e/dcron/internal/tui"
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

type fakeController struct {
	mu   []daemon.TriggerRequest
	resp daemon.TriggerResponse
	err  error
}

func (f *fakeController) Trigger(_ context.Context, req daemon.TriggerRequest) (daemon.TriggerResponse, error) {
	f.mu = append(f.mu, req)
	return f.resp, f.err
}

func newTestApp(t *testing.T, workDir string) (*App, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	tmp := t.TempDir()
	out := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}
	parser := schedule.NewHybridParser()

	app := &App{
		Registry:         registry.NewFileStore(filepath.Join(tmp, "registry.toml")),
		Loader:           config.NewTOMLLoader(parser),
		Parser:           parser,
		Runner:           runner.NewExecRunner(),
		StateStore:       state.NewFileStore(filepath.Join(tmp, "state.json")),
		ProcessChecker:   fakeProcessChecker(false),
		ServiceManager:   &fakeServiceManager{unitPath: filepath.Join(tmp, "dcron.service")},
		GlobalConfigPath: filepath.Join(tmp, "global.toml"),
		LogDir:           filepath.Join(tmp, "logs"),
		WorkDir:          func() (string, error) { return workDir, nil },
		Out:              out,
		Err:              errBuf,
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
	if canon, err := registry.CanonicalPath(repoDir); err == nil {
		repoDir = canon
	}
	app, out, errBuf := newTestApp(t, repoDir)

	// 1. init (creates .cron.toml and registers the repo automatically)
	if err := runCLI(t, app, out, errBuf, "init"); err != nil {
		t.Fatalf("init failed: %v", err)
	}
	if !strings.Contains(out.String(), "Created") || !strings.Contains(out.String(), `Registered "demo-repo"`) {
		t.Errorf("unexpected init output: %s", out.String())
	}

	// Re-running init with --force on an already-registered repo should succeed.
	if err := runCLI(t, app, out, errBuf, "init", "--force"); err != nil {
		t.Fatalf("init --force failed: %v", err)
	}
	if !strings.Contains(out.String(), "already registered") {
		t.Errorf("unexpected init --force output: %s", out.String())
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

[jobs.boot_delayed]
schedule = "5 minutes after reboot"
command = "echo booted-delayed"
`
	if err := os.WriteFile(filepath.Join(repoDir, config.FileName), []byte(customCfg), 0o644); err != nil {
		t.Fatal(err)
	}

	// 2. validate
	if err := runCLI(t, app, out, errBuf, "validate", "--next", "2"); err != nil {
		t.Fatalf("validate failed: %v", err)
	}
	if !strings.Contains(out.String(), "0 9 * * 1-5") || !strings.Contains(out.String(), "@reboot +5m") || !strings.Contains(out.String(), "5m0s after system boot") {
		t.Errorf("unexpected validate output:\n%s", out.String())
	}

	// 3. unregister + explicit register
	if err := runCLI(t, app, out, errBuf, "unregister", "demo-repo"); err != nil {
		t.Fatalf("unregister before re-register failed: %v", err)
	}
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

	// 5b. trigger via daemon controller (async and --wait)
	fc := &fakeController{
		resp: daemon.TriggerResponse{RepoName: "demo-repo", JobName: "greet", Completed: false},
	}
	app.Controller = fc
	if err := runCLI(t, app, out, errBuf, "trigger", "demo-repo/greet"); err != nil {
		t.Fatalf("trigger async failed: %v", err)
	}
	if !strings.Contains(out.String(), "Triggered demo-repo/greet in daemon") {
		t.Errorf("unexpected trigger async output: %q", out.String())
	}

	fc.resp = daemon.TriggerResponse{RepoName: "demo-repo", JobName: "greet", Completed: true, ExitCode: 0, DurationMs: 25}
	if err := runCLI(t, app, out, errBuf, "trigger", "--wait", "greet"); err != nil {
		t.Fatalf("trigger --wait failed: %v", err)
	}
	if !strings.Contains(out.String(), "Triggered and completed demo-repo/greet") {
		t.Errorf("unexpected trigger --wait output: %q", out.String())
	}

	// 5c. trigger fallback when daemon is stopped records run in state.json
	fc.err = daemon.ErrDaemonNotRunning
	if err := runCLI(t, app, out, errBuf, "trigger", "greet"); err != nil {
		t.Fatalf("trigger local fallback failed: %v", err)
	}
	if !strings.Contains(out.String(), "hello-from-dcron") || !strings.Contains(errBuf.String(), "Daemon is not running") {
		t.Errorf("unexpected fallback trigger output: out=%q err=%q", out.String(), errBuf.String())
	}
	if snapAfter, err := app.StateStore.Load(); err != nil || snapAfter.Repos[repoDir].Jobs["greet"].RunCount < 1 {
		t.Errorf("expected local trigger to update state RunCount, err=%v snap=%+v", err, snapAfter.Repos[repoDir].Jobs["greet"])
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
	delayedNext := now.Add(4 * time.Minute)
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
					"boot_delayed": {
						Name:            "boot_delayed",
						ScheduleSummary: "@reboot +5m",
						ScheduleKind:    "reboot",
						Enabled:         true,
						NextRun:         &delayedNext,
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
	if !strings.Contains(out.String(), "Daemon: running") || !strings.Contains(out.String(), "ok") || !strings.Contains(out.String(), delayedNext.Local().Format("2006-01-02 15:04:05")) {
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

	// 9. tui subcommand wiring
	var capturedDeps tui.Deps
	app.TUIRunner = func(_ context.Context, deps tui.Deps) error {
		capturedDeps = deps
		return nil
	}
	if err := runCLI(t, app, out, errBuf, "tui", "--repo", "demo-repo", "--refresh", "500ms"); err != nil {
		t.Fatalf("tui command failed: %v", err)
	}
	if capturedDeps.InitialRepo != repoDir || capturedDeps.RefreshInterval != 500*time.Millisecond {
		t.Errorf("unexpected tui deps: initialRepo=%q refresh=%s", capturedDeps.InitialRepo, capturedDeps.RefreshInterval)
	}
	if err := runCLI(t, app, out, errBuf, "tui", "--refresh", "0s"); err == nil {
		t.Errorf("expected error for non-positive --refresh")
	}

	// 10. unregister
	if err := runCLI(t, app, out, errBuf, "unregister", "demo-repo"); err != nil {
		t.Fatalf("unregister failed: %v", err)
	}
	if !strings.Contains(out.String(), `Unregistered "demo-repo"`) {
		t.Errorf("unexpected unregister output: %s", out.String())
	}
}

func TestCLI_GlobalCronWorkflow(t *testing.T) {
	workDir := t.TempDir()
	app, out, errBuf := newTestApp(t, workDir)
	globalPath, err := app.globalConfigPath()
	if err != nil {
		t.Fatal(err)
	}

	// Conflicting flags should fail cleanly.
	if err := runCLI(t, app, out, errBuf, "init", "--global", "--name", "foo"); err == nil {
		t.Fatal("expected error for init --global --name")
	}
	if err := runCLI(t, app, out, errBuf, "init", "--global", workDir); err == nil {
		t.Fatal("expected error for init --global <path>")
	}

	// 1. list -g before init shows helpful hint.
	if err := runCLI(t, app, out, errBuf, "list", "-g"); err != nil {
		t.Fatalf("list -g failed: %v", err)
	}
	if !strings.Contains(out.String(), "dcron init --global") {
		t.Errorf("expected init --global hint, got: %s", out.String())
	}

	// 2. init --global creates global.toml without adding to registry.
	if err := runCLI(t, app, out, errBuf, "init", "--global"); err != nil {
		t.Fatalf("init --global failed: %v", err)
	}
	if !strings.Contains(out.String(), "Created "+globalPath) {
		t.Errorf("unexpected init --global output: %s", out.String())
	}
	if entries, err := app.Registry.List(); err != nil || len(entries) != 0 {
		t.Fatalf("global init must not register in registry.toml, got entries=%+v err=%v", entries, err)
	}

	// Overwrite with custom global job.
	globalCfg := `
[jobs.cleanup]
description = "Global cleanup"
schedule = "daily at 9am"
command = "echo hello-global-cron"
`
	if err := os.WriteFile(globalPath, []byte(globalCfg), 0o644); err != nil {
		t.Fatal(err)
	}

	// 3. validate -g
	if err := runCLI(t, app, out, errBuf, "validate", "-g", "--next", "1"); err != nil {
		t.Fatalf("validate -g failed: %v", err)
	}
	if !strings.Contains(out.String(), "Valid: "+globalPath) || !strings.Contains(out.String(), "0 9 * * *") {
		t.Errorf("unexpected validate -g output:\n%s", out.String())
	}

	// 4. list and list -g both show global job.
	if err := runCLI(t, app, out, errBuf, "list"); err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if !strings.Contains(out.String(), "global") || !strings.Contains(out.String(), "cleanup") {
		t.Errorf("unexpected list output:\n%s", out.String())
	}
	if err := runCLI(t, app, out, errBuf, "list", "-g"); err != nil {
		t.Fatalf("list -g failed: %v", err)
	}
	if !strings.Contains(out.String(), "cleanup") {
		t.Errorf("unexpected list -g output:\n%s", out.String())
	}

	// 5. run via -g flag and via global/<job> shorthand.
	if err := runCLI(t, app, out, errBuf, "run", "-g", "cleanup"); err != nil {
		t.Fatalf("run -g cleanup failed: %v", err)
	}
	if !strings.Contains(out.String(), "hello-global-cron") {
		t.Errorf("unexpected run -g stdout: %q", out.String())
	}
	if err := runCLI(t, app, out, errBuf, "run", "global/cleanup"); err != nil {
		t.Fatalf("run global/cleanup failed: %v", err)
	}
	if !strings.Contains(out.String(), "hello-global-cron") {
		t.Errorf("unexpected run global/cleanup stdout: %q", out.String())
	}
	if err := runCLI(t, app, out, errBuf, "run", "-g", "--repo", "other", "cleanup"); err == nil {
		t.Fatal("expected error combining -g and --repo")
	}

	// 6. trigger via daemon controller and via local fallback.
	fc := &fakeController{
		resp: daemon.TriggerResponse{RepoName: "global", JobName: "cleanup"},
	}
	app.Controller = fc
	if err := runCLI(t, app, out, errBuf, "trigger", "-g", "cleanup"); err != nil {
		t.Fatalf("trigger -g failed: %v", err)
	}
	if len(fc.mu) != 1 || fc.mu[0].RepoPath != globalPath || fc.mu[0].JobName != "cleanup" {
		t.Fatalf("unexpected controller trigger request: %+v", fc.mu)
	}

	fc.err = daemon.ErrDaemonNotRunning
	if err := runCLI(t, app, out, errBuf, "trigger", "global/cleanup"); err != nil {
		t.Fatalf("trigger global/cleanup fallback failed: %v", err)
	}
	if snap, err := app.StateStore.Load(); err != nil || snap.Repos[globalPath].Jobs["cleanup"].RunCount < 1 {
		t.Errorf("expected local trigger to persist global job state, err=%v snap=%+v", err, snap.Repos[globalPath])
	}

	// 7. logs -g and logs global/cleanup
	if err := runCLI(t, app, out, errBuf, "logs", "-g", "cleanup"); err != nil {
		t.Fatalf("logs -g failed: %v", err)
	}
	if !strings.Contains(out.String(), "hello-global-cron") {
		t.Errorf("unexpected logs -g output:\n%s", out.String())
	}
	if err := runCLI(t, app, out, errBuf, "logs", "global/cleanup"); err != nil {
		t.Fatalf("logs global/cleanup failed: %v", err)
	}

	// 8. status and status -g
	if err := runCLI(t, app, out, errBuf, "status", "-g"); err != nil {
		t.Fatalf("status -g failed: %v", err)
	}
	if !strings.Contains(out.String(), "global") || !strings.Contains(out.String(), "cleanup") {
		t.Errorf("unexpected status -g output:\n%s", out.String())
	}

	// 9. tui -g wiring
	var capturedDeps tui.Deps
	app.TUIRunner = func(_ context.Context, deps tui.Deps) error {
		capturedDeps = deps
		return nil
	}
	if err := runCLI(t, app, out, errBuf, "tui", "-g"); err != nil {
		t.Fatalf("tui -g failed: %v", err)
	}
	if capturedDeps.InitialRepo != globalPath || capturedDeps.GlobalConfigPath != app.GlobalConfigPath {
		t.Errorf("unexpected tui -g deps: initialRepo=%q globalConfigPath=%q", capturedDeps.InitialRepo, capturedDeps.GlobalConfigPath)
	}
	if err := runCLI(t, app, out, errBuf, "tui", "-g", "--repo", "foo"); err == nil {
		t.Fatal("expected error combining tui -g and --repo")
	}
}

var _ daemon.ServiceManager = (*fakeServiceManager)(nil)
