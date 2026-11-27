package daemon

import (
	"context"
	"errors"
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

type staticBootInfo struct {
	id       string
	bootTime time.Time
}

func (b staticBootInfo) BootID() (string, error)      { return b.id, nil }
func (b staticBootInfo) BootTime() (time.Time, error) { return b.bootTime, nil }

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestService_ValidateBeforeSwapAndBootIDDedup(t *testing.T) {
	tmp := t.TempDir()
	repoDir := filepath.Join(tmp, "myrepo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if canon, err := registry.CanonicalPath(repoDir); err == nil {
		repoDir = canon
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
	if canon, err := registry.CanonicalPath(repoDir); err == nil {
		repoDir = canon
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

func TestService_ManualTriggerOverControlHTTP(t *testing.T) {
	tmp := t.TempDir()
	repoDir := filepath.Join(tmp, "trig-repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if canon, err := registry.CanonicalPath(repoDir); err == nil {
		repoDir = canon
	}

	cfg := `
[jobs.sync]
schedule = "every hour"
command = "echo manual-sync"
`
	if err := os.WriteFile(filepath.Join(repoDir, config.FileName), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	regStore := registry.NewFileStore(filepath.Join(tmp, "registry.toml"))
	if _, err := regStore.Add(repoDir, "trig-repo"); err != nil {
		t.Fatal(err)
	}
	stateStore := state.NewFileStore(filepath.Join(tmp, "state.json"))
	controlPath := DefaultControlPath(tmp)
	client := NewHTTPControlClient(controlPath)

	// Before daemon starts, client should report ErrDaemonNotRunning.
	if _, err := client.Trigger(context.Background(), TriggerRequest{RepoPath: repoDir, JobName: "sync"}); !errors.Is(err, ErrDaemonNotRunning) {
		t.Fatalf("expected ErrDaemonNotRunning before start, got: %v", err)
	}

	var calls atomic.Int32
	gate := make(chan struct{})
	runningCh := make(chan struct{}, 2)
	runr := runnerFunc(func(_ context.Context, req runner.Request) runner.Result {
		n := calls.Add(1)
		if n == 2 {
			runningCh <- struct{}{}
			<-gate
		}
		now := time.Now().UTC()
		return runner.Result{
			StartedAt:  now,
			FinishedAt: now.Add(5 * time.Millisecond),
			Duration:   5 * time.Millisecond,
			ExitCode:   0,
			LogPath:    req.LogPath,
		}
	})

	sched := &fakeScheduler{}
	svc, err := NewService(Options{
		Registry:    regStore,
		Loader:      config.NewTOMLLoader(schedule.NewHybridParser()),
		Runner:      runr,
		Scheduler:   sched,
		StateStore:  stateStore,
		BootID:      staticBootID("boot-trig"),
		LogDir:      filepath.Join(tmp, "logs"),
		ControlPath: controlPath,
		Logger:      silentLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- svc.Run(ctx)
	}()

	waitFor(t, 2*time.Second, func() bool {
		_, err := os.Stat(controlPath)
		return err == nil
	}, "control.json created")

	// 1. Synchronous trigger (--wait).
	resp, err := client.Trigger(context.Background(), TriggerRequest{
		RepoPath: repoDir,
		JobName:  "sync",
		Wait:     true,
	})
	if err != nil {
		t.Fatalf("sync Trigger failed: %v", err)
	}
	if !resp.Completed || resp.ExitCode != 0 || resp.RepoName != "trig-repo" {
		t.Fatalf("unexpected sync trigger response: %+v", resp)
	}
	if got := svc.Snapshot().Repos[repoDir].Jobs["sync"].RunCount; got != 1 {
		t.Fatalf("expected RunCount=1, got %d", got)
	}

	// 2. Async trigger while blocked on gate, then verify overlap=skip rejects a concurrent trigger.
	resp, err = client.Trigger(context.Background(), TriggerRequest{
		RepoPath: repoDir,
		JobName:  "sync",
		Wait:     false,
	})
	if err != nil {
		t.Fatalf("async Trigger failed: %v", err)
	}
	if resp.Completed {
		t.Fatalf("expected async trigger Completed=false")
	}
	<-runningCh

	if _, err := client.Trigger(context.Background(), TriggerRequest{
		RepoPath: repoDir,
		JobName:  "sync",
		Wait:     false,
	}); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("expected already running error on overlap=skip, got: %v", err)
	}

	close(gate)
	waitFor(t, 2*time.Second, func() bool {
		return svc.Snapshot().Repos[repoDir].Jobs["sync"].RunCount == 2
	}, "second triggered run completed")

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() error: %v", err)
	}
}

func TestService_DelayedRebootScheduling(t *testing.T) {
	tmp := t.TempDir()
	repoDir := filepath.Join(tmp, "reboot-repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if canon, err := registry.CanonicalPath(repoDir); err == nil {
		repoDir = canon
	}

	cfg := `
[jobs.after_boot]
schedule = "5 minutes after reboot"
command = "echo delayed"

[jobs.already_elapsed]
schedule = "30 seconds after reboot"
command = "echo elapsed"
`
	if err := os.WriteFile(filepath.Join(repoDir, config.FileName), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	regStore := registry.NewFileStore(filepath.Join(tmp, "registry.toml"))
	if _, err := regStore.Add(repoDir, "reboot-repo"); err != nil {
		t.Fatal(err)
	}
	stateStore := state.NewFileStore(filepath.Join(tmp, "state.json"))
	sched := &fakeScheduler{}
	runr := &fakeRunner{}

	bootTime := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	now := bootTime.Add(1 * time.Minute) // 1 minute since OS boot

	svc, err := NewService(Options{
		Registry:   regStore,
		Loader:     config.NewTOMLLoader(schedule.NewHybridParser()),
		Runner:     runr,
		Scheduler:  sched,
		StateStore: stateStore,
		BootID:     staticBootInfo{id: "boot-delay-1", bootTime: bootTime},
		LogDir:     filepath.Join(tmp, "logs"),
		Logger:     silentLogger(),
		Now:        func() time.Time { return now },
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

	if got := sched.count(); got != 2 {
		t.Fatalf("scheduled jobs = %d, want 2", got)
	}

	wantFuture := bootTime.Add(5 * time.Minute)
	wantPast := bootTime.Add(30 * time.Second)
	sched.mu.Lock()
	specs := make(map[string]JobSpec, len(sched.entries))
	for _, e := range sched.entries {
		specs[e.spec.Name] = e.spec
	}
	sched.mu.Unlock()

	if !specs["reboot-repo/after_boot"].RunAt.Equal(wantFuture) {
		t.Errorf("after_boot RunAt = %v, want %v", specs["reboot-repo/after_boot"].RunAt, wantFuture)
	}
	if !specs["reboot-repo/already_elapsed"].RunAt.Equal(wantPast) {
		t.Errorf("already_elapsed RunAt = %v, want %v", specs["reboot-repo/already_elapsed"].RunAt, wantPast)
	}

	snap := svc.Snapshot()
	afterJob := snap.Repos[repoDir].Jobs["after_boot"]
	if afterJob.NextRun == nil || !afterJob.NextRun.Equal(wantFuture) {
		t.Errorf("after_boot NextRun = %v, want %v", afterJob.NextRun, wantFuture)
	}
	if afterJob.ScheduleSummary != "@reboot +5m" {
		t.Errorf("after_boot ScheduleSummary = %q, want @reboot +5m", afterJob.ScheduleSummary)
	}
	elapsedJob := snap.Repos[repoDir].Jobs["already_elapsed"]
	if elapsedJob.NextRun != nil {
		t.Errorf("already_elapsed NextRun = %v, want nil (already past delay)", elapsedJob.NextRun)
	}

	// Reloading 1 minute later (still before bootTime+5m) must preserve the exact same RunAt.
	now = bootTime.Add(2 * time.Minute)
	if err := svc.ReloadRepo(context.Background(), repoDir); err != nil {
		t.Fatalf("ReloadRepo: %v", err)
	}
	snap = svc.Snapshot()
	if snap.Repos[repoDir].Jobs["after_boot"].NextRun == nil || !snap.Repos[repoDir].Jobs["after_boot"].NextRun.Equal(wantFuture) {
		t.Errorf("after reload, NextRun = %v, want %v", snap.Repos[repoDir].Jobs["after_boot"].NextRun, wantFuture)
	}

	// Executing clears NextRun, records LastBootID, and prevents re-scheduling on same boot.
	sched.runAll(context.Background())
	snap = svc.Snapshot()
	afterJob = snap.Repos[repoDir].Jobs["after_boot"]
	if afterJob.NextRun != nil {
		t.Errorf("expected NextRun=nil after execution, got %v", afterJob.NextRun)
	}
	if afterJob.LastBootID != "boot-delay-1" || afterJob.RunCount != 1 {
		t.Errorf("unexpected after_boot state after run: %+v", afterJob)
	}

	if err := svc.ReloadRepo(context.Background(), repoDir); err != nil {
		t.Fatalf("ReloadRepo after run: %v", err)
	}
	if got := sched.count(); got != 0 {
		t.Fatalf("expected 0 reboot jobs re-scheduled in same boot, got %d", got)
	}
}

func TestGoCronScheduler_DelayedRebootRunsAfterDelay(t *testing.T) {
	tmp := t.TempDir()
	repoDir := filepath.Join(tmp, "delayed-live")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := `
[jobs.delayed]
schedule = "150ms after reboot"
command = "echo delayed"
`
	if err := os.WriteFile(filepath.Join(repoDir, config.FileName), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	regStore := registry.NewFileStore(filepath.Join(tmp, "registry.toml"))
	if _, err := regStore.Add(repoDir, "delayed-live"); err != nil {
		t.Fatal(err)
	}
	stateStore := state.NewFileStore(filepath.Join(tmp, "state.json"))
	sched, err := NewGoCronScheduler(time.UTC)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	ranAtCh := make(chan time.Time, 2)
	runr := runnerFunc(func(_ context.Context, req runner.Request) runner.Result {
		ranAtCh <- time.Now()
		now := time.Now().UTC()
		return runner.Result{StartedAt: now, FinishedAt: now, ExitCode: 0}
	})

	svc, err := NewService(Options{
		Registry:   regStore,
		Loader:     config.NewTOMLLoader(schedule.NewHybridParser()),
		Runner:     runr,
		Scheduler:  sched,
		StateStore: stateStore,
		BootID:     staticBootInfo{id: "boot-live-delay", bootTime: start},
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

	select {
	case ranAt := <-ranAtCh:
		if elapsed := ranAt.Sub(start); elapsed < 100*time.Millisecond {
			t.Errorf("delayed reboot job ran too early: %v", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for delayed reboot job to execute")
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() error: %v", err)
	}
}

func TestService_GlobalConfigAutoWatchReloadAndUnload(t *testing.T) {
	tmp := t.TempDir()
	cfgDir := filepath.Join(tmp, "config")
	regStore := registry.NewFileStore(filepath.Join(cfgDir, "registry.toml"))
	globalPath := filepath.Join(cfgDir, config.GlobalFileName)
	if canon, err := registry.CanonicalPath(globalPath); err == nil {
		globalPath = canon
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
	var lastRepoName atomic.Value
	runr := runnerFunc(func(_ context.Context, req runner.Request) runner.Result {
		lastRepoName.Store(req.RepoName)
		if req.Job.Name == "heartbeat" {
			ran.Add(1)
		}
		now := time.Now().UTC()
		return runner.Result{StartedAt: now, FinishedAt: now, ExitCode: 0}
	})

	svc, err := NewService(Options{
		Registry:         regStore,
		Loader:           config.NewTOMLLoader(schedule.NewHybridParser()),
		Runner:           runr,
		Scheduler:        sched,
		Watcher:          w,
		StateStore:       stateStore,
		BootID:           staticBootID("boot-global"),
		GlobalConfigPath: globalPath,
		LogDir:           filepath.Join(tmp, "logs"),
		Logger:           silentLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- svc.Run(ctx)
	}()

	// Wait until daemon finishes initial load (global.toml does not exist yet).
	waitFor(t, 2*time.Second, func() bool {
		return svc.Snapshot().PID != 0
	}, "daemon started")

	if _, exists := svc.Snapshot().Repos[globalPath]; exists {
		t.Fatalf("global should not be in snapshot before global.toml is created")
	}

	// Create global.toml live: daemon should auto-detect, schedule, and run it.
	validGlobal := `
[jobs.heartbeat]
schedule = "every 1s"
command = "echo global-ping"
`
	if err := os.WriteFile(globalPath, []byte(validGlobal), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool {
		return ran.Load() >= 1
	}, "global job 'heartbeat' auto-loaded and executed")

	if got, _ := lastRepoName.Load().(string); got != config.GlobalRepoName {
		t.Errorf("global job RepoName = %q, want %q", got, config.GlobalRepoName)
	}

	// Manual TriggerJob should work for global jobs.
	resp, err := svc.TriggerJob(context.Background(), TriggerRequest{
		RepoPath: globalPath,
		JobName:  "heartbeat",
		Wait:     true,
	})
	if err != nil || !resp.Completed || resp.RepoName != config.GlobalRepoName {
		t.Fatalf("TriggerJob(global) = (%+v, %v), want completed with repo=global", resp, err)
	}

	// Broken global.toml preserves existing schedule and records ConfigError.
	if err := os.WriteFile(globalPath, []byte("[jobs.heartbeat]\nschedule = 'bad'\ncommand = 'echo'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool {
		return strings.Contains(svc.Snapshot().Repos[globalPath].ConfigError, "unrecognized schedule")
	}, "global ConfigError recorded")

	// Deleting global.toml unloads global jobs cleanly without removing the watcher.
	if err := os.Remove(globalPath); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool {
		_, exists := svc.Snapshot().Repos[globalPath]
		return !exists
	}, "global config unloaded after deletion")

	// Re-creating global.toml after deletion is still picked up automatically.
	recreated := `
[jobs.reborn]
schedule = "every hour"
command = "echo reborn"
`
	if err := os.WriteFile(globalPath, []byte(recreated), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool {
		_, ok := svc.Snapshot().Repos[globalPath].Jobs["reborn"]
		return ok
	}, "global config reloaded after re-creation")

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() error: %v", err)
	}
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
