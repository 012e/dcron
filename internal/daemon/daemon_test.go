package daemon

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/012e/dcron/internal/config"
	"github.com/012e/dcron/internal/registry"
	"github.com/012e/dcron/internal/runner"
	"github.com/012e/dcron/internal/schedule"
	"github.com/012e/dcron/internal/state"
	"github.com/012e/dcron/internal/watcher"
)

// --- Deterministic test fakes ---

type fakeHandle struct {
	next time.Time
}

func (f fakeHandle) NextRun() (time.Time, error) { return f.next, nil }
func (f fakeHandle) LastRun() (time.Time, error) { return time.Time{}, nil }

type scheduledEntry struct {
	spec JobSpec
	task func(ctx context.Context)
}

type fakeScheduler struct {
	mu      sync.Mutex
	started bool
	entries []scheduledEntry
}

func (f *fakeScheduler) Start() {
	f.mu.Lock()
	f.started = true
	f.mu.Unlock()
}

func (f *fakeScheduler) Shutdown() error { return nil }

func (f *fakeScheduler) ScheduleJob(spec JobSpec, task func(ctx context.Context)) (JobHandle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, scheduledEntry{spec: spec, task: task})
	return fakeHandle{next: time.Now().Add(time.Hour)}, nil
}

func (f *fakeScheduler) RemoveByTag(tag string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	kept := f.entries[:0]
	for _, e := range f.entries {
		if e.spec.Tag != tag {
			kept = append(kept, e)
		}
	}
	f.entries = kept
	return nil
}

func (f *fakeScheduler) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.entries)
}

func (f *fakeScheduler) runAll(ctx context.Context) {
	f.mu.Lock()
	copied := append([]scheduledEntry(nil), f.entries...)
	f.mu.Unlock()
	for _, e := range copied {
		e.task(ctx)
	}
}

type fakeRunner struct {
	mu    sync.Mutex
	calls []runner.Request
}

func (f *fakeRunner) Run(_ context.Context, req runner.Request) runner.Result {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	f.mu.Unlock()
	now := time.Now().UTC()
	return runner.Result{
		StartedAt:  now,
		FinishedAt: now.Add(10 * time.Millisecond),
		Duration:   10 * time.Millisecond,
		ExitCode:   0,
		LogPath:    req.LogPath,
	}
}

type staticBootID string

func (b staticBootID) BootID() (string, error) { return string(b), nil }

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestService_ValidateBeforeSwapAndBootIDDedup(t *testing.T) {
	tmp := t.TempDir()
	repoDir := filepath.Join(tmp, "myrepo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	validTOML := `
[jobs.sync]
schedule = "every hour"
command = "echo sync"

[jobs.warmup]
schedule = "on reboot"
command = "echo boot"

[jobs.disabled_job]
schedule = "every minute"
command = "echo off"
enabled = false
`
	cfgPath := filepath.Join(repoDir, config.FileName)
	if err := os.WriteFile(cfgPath, []byte(validTOML), 0o644); err != nil {
		t.Fatal(err)
	}

	regStore := registry.NewFileStore(filepath.Join(tmp, "registry.toml"))
	if _, err := regStore.Add(repoDir, "myrepo"); err != nil {
		t.Fatal(err)
	}
	stateStore := state.NewFileStore(filepath.Join(tmp, "state.json"))
	sched := &fakeScheduler{}
	runr := &fakeRunner{}

	svc, err := NewService(Options{
		Registry:   regStore,
		Loader:     config.NewTOMLLoader(schedule.NewHybridParser()),
		Runner:     runr,
		Scheduler:  sched,
		StateStore: stateStore,
		BootID:     staticBootID("boot-1"),
		LogDir:     filepath.Join(tmp, "logs"),
		Logger:     silentLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.initState(); err != nil {
		t.Fatal(err)
	}
	if err := svc.ReloadAll(context.Background()); err != nil {
		t.Fatalf("ReloadAll: %v", err)
	}

	// Only enabled jobs (sync + warmup) should be scheduled; disabled_job is not.
	if got := sched.count(); got != 2 {
		t.Fatalf("scheduled jobs = %d, want 2", got)
	}

	// Execute scheduled jobs (marks warmup as run for boot-1).
	sched.runAll(context.Background())

	snap := svc.Snapshot()
	rs := snap.Repos[repoDir]
	if rs.Jobs["warmup"].LastBootID != "boot-1" || rs.Jobs["warmup"].RunCount != 1 {
		t.Errorf("unexpected warmup state after boot run: %+v", rs.Jobs["warmup"])
	}

	// Reloading during the SAME boot should NOT re-schedule the on-reboot job!
	if err := svc.ReloadRepo(context.Background(), repoDir); err != nil {
		t.Fatalf("ReloadRepo same boot: %v", err)
	}
	if got := sched.count(); got != 1 {
		t.Fatalf("expected only 1 job (sync) re-scheduled on same boot, got %d", got)
	}

	// Now write a broken .cron.toml: validate-before-swap must preserve the existing job
	// AND record ConfigError in state!
	if err := os.WriteFile(cfgPath, []byte("[jobs.sync]\nschedule = 'nonsense'\ncommand = 'echo'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := svc.ReloadRepo(context.Background(), repoDir); err == nil {
		t.Fatal("expected error reloading invalid config")
	}
	if got := sched.count(); got != 1 {
		t.Fatalf("existing job should still be scheduled on invalid reload, got %d", got)
	}
	snap = svc.Snapshot()
	if !strings.Contains(snap.Repos[repoDir].ConfigError, "unrecognized schedule") {
		t.Errorf("expected ConfigError in snapshot, got %q", snap.Repos[repoDir].ConfigError)
	}
}

func TestService_IntegrationAutoReloadWithGoCronAndFSNotify(t *testing.T) {
	tmp := t.TempDir()
	repoDir := filepath.Join(tmp, "live-repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfgPath := filepath.Join(repoDir, config.FileName)
	initial := `
[jobs.ping]
schedule = "every hour"
command = "echo v1"
`
	if err := os.WriteFile(cfgPath, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	regStore := registry.NewFileStore(filepath.Join(tmp, "config", "registry.toml"))
	if _, err := regStore.Add(repoDir, "live-repo"); err != nil {
		t.Fatal(err)
	}
	stateStore := state.NewFileStore(filepath.Join(tmp, "state", "state.json"))

	sched, err := NewGoCronScheduler(time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	w, err := watcher.NewFSNotifyWatcher(30 * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}

	var ran atomic.Int32
	customRunner := runnerFunc(func(_ context.Context, req runner.Request) runner.Result {
		if req.Job.Name == "fast" {
			ran.Add(1)
		}
		now := time.Now().UTC()
		return runner.Result{StartedAt: now, FinishedAt: now, ExitCode: 0}
	})

	svc, err := NewService(Options{
		Registry:   regStore,
		Loader:     config.NewTOMLLoader(schedule.NewHybridParser()),
		Runner:     customRunner,
		Scheduler:  sched,
		Watcher:    w,
		StateStore: stateStore,
		BootID:     staticBootID("boot-integration"),
		LogDir:     filepath.Join(tmp, "logs"),
		Logger:     silentLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- svc.Run(ctx)
	}()

	// Wait until initial snapshot is loaded.
	waitFor(t, 2*time.Second, func() bool {
		_, ok := svc.Snapshot().Repos[repoDir].Jobs["ping"]
		return ok
	}, "initial job 'ping' loaded")

	// Live-edit .cron.toml to add a fast 1s interval job and verify auto-reload picks it up and executes it!
	updated := `
[jobs.fast]
schedule = "every 1s"
command = "echo v2"
`
	if err := os.WriteFile(cfgPath, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}

	waitFor(t, 3*time.Second, func() bool {
		return ran.Load() >= 1
	}, "auto-reloaded job 'fast' executed by gocron")

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
}

type runnerFunc func(ctx context.Context, req runner.Request) runner.Result

func (f runnerFunc) Run(ctx context.Context, req runner.Request) runner.Result {
	return f(ctx, req)
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, desc string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", desc)
}
