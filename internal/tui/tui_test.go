package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/012e/dcron/internal/config"
	"github.com/012e/dcron/internal/registry"
	"github.com/012e/dcron/internal/runner"
	"github.com/012e/dcron/internal/schedule"
	"github.com/012e/dcron/internal/state"
)

type fakeProcessChecker bool

func (f fakeProcessChecker) IsAlive(int) bool { return bool(f) }

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

func setupTestEnv(t *testing.T) (Deps, string, string) {
	t.Helper()
	tmp := t.TempDir()

	repoA := filepath.Join(tmp, "alpha")
	repoB := filepath.Join(tmp, "beta")
	if err := os.MkdirAll(repoA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repoB, 0o755); err != nil {
		t.Fatal(err)
	}

	cfgA := `
[defaults]
timeout = "1m"

[jobs.sync]
description = "Sync repository files"
schedule = "every hour"
command = "echo alpha-sync-ok"

[jobs.boot]
description = "Boot warmup"
schedule = "on reboot"
command = "echo booted"
`
	if err := os.WriteFile(filepath.Join(repoA, config.FileName), []byte(cfgA), 0o644); err != nil {
		t.Fatal(err)
	}

	cfgB := `
[jobs.report]
description = "Generate report"
schedule = "every weekday at 9am"
command = "echo beta-report"
`
	if err := os.WriteFile(filepath.Join(repoB, config.FileName), []byte(cfgB), 0o644); err != nil {
		t.Fatal(err)
	}

	parser := schedule.NewHybridParser()
	loader := config.NewTOMLLoader(parser)
	reg := registry.NewFileStore(filepath.Join(tmp, "registry.toml"))
	st := state.NewFileStore(filepath.Join(tmp, "state.json"))
	logDir := filepath.Join(tmp, "logs")

	if _, err := reg.Add(repoA, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Add(repoB, ""); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	okCode := 0
	failCode := 2
	nextRun := now.Add(25 * time.Minute)
	lastRun := now.Add(-10 * time.Minute)

	if err := st.Save(state.DaemonSnapshot{
		PID:       4242,
		StartedAt: now.Add(-2 * time.Hour),
		UpdatedAt: now,
		BootID:    "boot-123",
		Repos: map[string]state.RepoState{
			repoA: {
				Name: "alpha",
				Path: repoA,
				Jobs: map[string]state.JobState{
					"sync": {
						Name:            "sync",
						ScheduleRaw:     "every hour",
						ScheduleSummary: "0 * * * *",
						ScheduleKind:    "cron",
						Enabled:         true,
						LastRun:         &lastRun,
						LastDurationMs:  350,
						LastExitCode:    &okCode,
						NextRun:         &nextRun,
						RunCount:        4,
						LogPath:         runner.LogPathFor(logDir, "alpha", repoA, "sync"),
					},
					"boot": {
						Name:            "boot",
						ScheduleRaw:     "on reboot",
						ScheduleSummary: "@reboot",
						ScheduleKind:    "reboot",
						Enabled:         true,
						LastBootID:      "boot-123",
						RunCount:        1,
						LogPath:         runner.LogPathFor(logDir, "alpha", repoA, "boot"),
					},
				},
			},
			repoB: {
				Name: "beta",
				Path: repoB,
				Jobs: map[string]state.JobState{
					"report": {
						Name:            "report",
						ScheduleRaw:     "every weekday at 9am",
						ScheduleSummary: "0 9 * * 1-5",
						ScheduleKind:    "cron",
						Enabled:         true,
						LastRun:         &lastRun,
						LastExitCode:    &failCode,
						LastError:       "exit status 2",
						RunCount:        1,
						LogPath:         runner.LogPathFor(logDir, "beta", repoB, "report"),
					},
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	deps := Deps{
		Registry:       reg,
		Loader:         loader,
		Parser:         parser,
		Runner:         runner.NewExecRunner(),
		StateStore:     st,
		ProcessChecker: fakeProcessChecker(true),
		ServiceManager: &fakeServiceManager{unitPath: filepath.Join(tmp, "dcron.service")},
		LogDir:         logDir,
		WorkDir:        func() (string, error) { return repoA, nil },
	}
	return deps, repoA, repoB
}

func sendKey(m Model, k string) (Model, tea.Cmd) {
	var msg tea.KeyMsg
	switch k {
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		msg = tea.KeyMsg{Type: tea.KeyTab}
	case "ctrl+n":
		msg = tea.KeyMsg{Type: tea.KeyCtrlN}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
	}
	updated, cmd := m.Update(msg)
	return updated.(Model), cmd
}

func loadInitialModel(t *testing.T, deps Deps, w, h int) Model {
	t.Helper()
	m := NewModel(deps)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m = updated.(Model)

	snapMsg := m.loadSnapshotCmd()()
	updated, logCmd := m.Update(snapMsg)
	m = updated.(Model)
	if logCmd != nil {
		updated, _ = m.Update(logCmd())
		m = updated.(Model)
	}
	return m
}

func TestLoadSnapshotAndFormatting(t *testing.T) {
	deps, _, _ := setupTestEnv(t)
	now := time.Now().UTC()

	snap, err := LoadSnapshot(deps, now)
	if err != nil {
		t.Fatalf("LoadSnapshot failed: %v", err)
	}
	if !snap.DaemonAlive || snap.DaemonPID != 4242 {
		t.Fatalf("expected daemon alive with pid 4242, got alive=%v pid=%d", snap.DaemonAlive, snap.DaemonPID)
	}
	if len(snap.Repos) != 2 {
		t.Fatalf("expected 2 repos, got %d", len(snap.Repos))
	}
	if len(snap.AllJobs) != 3 {
		t.Fatalf("expected 3 total jobs, got %d", len(snap.AllJobs))
	}
	if snap.TotalFailed != 1 {
		t.Fatalf("expected 1 failed job, got %d", snap.TotalFailed)
	}

	if got := FormatRelativeTime(now.Add(-90*time.Second), now); got != "1m30s ago" {
		t.Errorf("unexpected past relative time: %q", got)
	}
	if got := FormatRelativeTime(now.Add(2*time.Hour+15*time.Minute), now); got != "in 2h15m" {
		t.Errorf("unexpected future relative time: %q", got)
	}
	if got := FormatDurationMs(450); got != "450ms" {
		t.Errorf("unexpected ms duration: %q", got)
	}
}

func TestReadLogTail(t *testing.T) {
	tmp := t.TempDir()
	missing := filepath.Join(tmp, "missing.log")
	if _, exists, err := ReadLogTail(missing, 10); err != nil || exists {
		t.Fatalf("expected missing file to return exists=false, err=nil; got exists=%v err=%v", exists, err)
	}

	logFile := filepath.Join(tmp, "job.log")
	lines := []string{"line1", "line2", "line3", "line4", "line5"}
	if err := os.WriteFile(logFile, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	content, exists, err := ReadLogTail(logFile, 3)
	if err != nil || !exists {
		t.Fatalf("ReadLogTail failed: exists=%v err=%v", exists, err)
	}
	if content != "line3\nline4\nline5" {
		t.Fatalf("unexpected tail content: %q", content)
	}
}

func TestModelRenderWideAndNarrow(t *testing.T) {
	deps, _, _ := setupTestEnv(t)

	wide := loadInitialModel(t, deps, 120, 36)
	view := wide.View()
	for _, want := range []string{"dcron", "DAEMON RUNNING", "All Repositories", "alpha", "beta", "sync", "report", "Details (d)", "Logs (L)"} {
		if !strings.Contains(view, want) {
			t.Errorf("wide view missing %q", want)
		}
	}

	narrow := loadInitialModel(t, deps, 72, 24)
	narrowView := narrow.View()
	for _, want := range []string{"dcron", "alpha", "sync"} {
		if !strings.Contains(narrowView, want) {
			t.Errorf("narrow view missing %q", want)
		}
	}
}

func TestModelNavigationFilteringAndRunJob(t *testing.T) {
	deps, _, _ := setupTestEnv(t)
	m := loadInitialModel(t, deps, 120, 36)

	// Switch to Repos pane and select "alpha" (index 1).
	m, _ = sendKey(m, "1")
	m, _ = sendKey(m, "j")
	if m.repoIdx != 1 {
		t.Fatalf("expected repoIdx=1, got %d", m.repoIdx)
	}
	if jobs := m.filteredJobs(); len(jobs) != 2 {
		t.Fatalf("expected 2 jobs for alpha, got %d", len(jobs))
	}

	// Filter to "sync".
	m, _ = sendKey(m, "/")
	for _, ch := range "sync" {
		m, _ = sendKey(m, string(ch))
	}
	m, _ = sendKey(m, "enter")
	jobs := m.filteredJobs()
	if len(jobs) != 1 || jobs[0].Name != "sync" {
		t.Fatalf("expected filtered job 'sync', got %+v", jobs)
	}

	// Trigger immediate job run via 'x'.
	m, runCmd := sendKey(m, "x")
	if runCmd == nil {
		t.Fatal("expected run command when pressing x")
	}
	if !m.isManualRunning(jobs[0].Key()) {
		t.Fatal("expected job to be marked running")
	}

	// Execute the command synchronously and feed completion msg back to model.
	finishedMsg := runCmd()
	updated, followUpCmd := m.Update(finishedMsg)
	m = updated.(Model)
	if m.isManualRunning(jobs[0].Key()) {
		t.Fatal("expected job to no longer be marked running after completion")
	}
	if !strings.Contains(m.toastMsg, "completed") {
		t.Fatalf("expected completion toast, got %q", m.toastMsg)
	}
	_ = followUpCmd

	// Open Logs tab and load logs — should contain output from the run!
	m, logCmd := sendKey(m, "L")
	if m.inspectorTab != tabLogs {
		t.Fatalf("expected Logs tab active")
	}
	if logCmd != nil {
		updated, _ = m.Update(logCmd())
		m = updated.(Model)
	}
	if !strings.Contains(m.logRaw, "alpha-sync-ok") {
		t.Fatalf("expected job log output to contain 'alpha-sync-ok', got %q", m.logRaw)
	}
}

func TestModelModalsAddUnregisterValidateAndDaemon(t *testing.T) {
	deps, _, _ := setupTestEnv(t)
	m := loadInitialModel(t, deps, 120, 36)

	// Help modal toggles cleanly.
	m, _ = sendKey(m, "?")
	if m.modal != modalHelp || !strings.Contains(m.View(), "Keyboard Shortcuts") {
		t.Fatalf("expected help modal visible")
	}
	m, _ = sendKey(m, "esc")
	if m.modal != modalNone {
		t.Fatalf("expected help modal closed")
	}

	// Add a brand new repo without .cron.toml using Ctrl+N to initialize starter template & register.
	gammaDir := filepath.Join(t.TempDir(), "gamma")
	if err := os.MkdirAll(gammaDir, 0o755); err != nil {
		t.Fatal(err)
	}

	m, _ = sendKey(m, "a")
	if m.modal != modalAddRepo {
		t.Fatalf("expected modalAddRepo")
	}
	m.addPathInput.SetValue(gammaDir)
	m, reloadCmd := sendKey(m, "ctrl+n")
	if m.modal != modalNone {
		t.Fatalf("expected add modal to close on success, err=%q", m.addErr)
	}
	if reloadCmd != nil {
		updated, _ := m.Update(reloadCmd())
		m = updated.(Model)
	}
	if len(m.snap.Repos) != 3 {
		t.Fatalf("expected 3 registered repos after adding gamma, got %d", len(m.snap.Repos))
	}

	// Validate currently selected repo (gamma).
	m, _ = sendKey(m, "v")
	if !strings.Contains(m.toastMsg, "valid") {
		t.Fatalf("expected valid toast, got %q", m.toastMsg)
	}

	// Unregister gamma via confirmation modal.
	m, _ = sendKey(m, "u")
	if m.modal != modalConfirmUnregister {
		t.Fatalf("expected modalConfirmUnregister")
	}
	m, reloadCmd = sendKey(m, "y")
	if reloadCmd != nil {
		updated, _ := m.Update(reloadCmd())
		m = updated.(Model)
	}
	if len(m.snap.Repos) != 2 {
		t.Fatalf("expected 2 repos after unregistering gamma, got %d", len(m.snap.Repos))
	}

	// Open Daemon modal and run install/uninstall.
	m, statusCmd := sendKey(m, "s")
	if m.modal != modalDaemon || statusCmd == nil {
		t.Fatalf("expected modalDaemon and status command")
	}
	updated, _ := m.Update(statusCmd())
	m = updated.(Model)
	if !strings.Contains(m.View(), "inactive (dead)") {
		t.Fatalf("expected inactive daemon status in modal")
	}

	m, installCmd := sendKey(m, "i")
	if installCmd == nil {
		t.Fatal("expected install command")
	}
	updated, _ = m.Update(installCmd())
	m = updated.(Model)
	if !strings.Contains(m.toastMsg, "Installed") {
		t.Fatalf("expected install toast, got %q", m.toastMsg)
	}
}
