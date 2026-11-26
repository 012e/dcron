package e2e

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var dcronBin string

func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "dcron-e2e-bin-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create temp dir for e2e binary: %v\n", err)
		os.Exit(1)
	}

	binName := "dcron"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	dcronBin = filepath.Join(tmpDir, binName)

	buildCmd := exec.Command("go", "build", "-o", dcronBin, "..")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build dcron binary for e2e tests: %v\n%s\n", err, string(out))
		_ = os.RemoveAll(tmpDir)
		os.Exit(1)
	}

	code := m.Run()
	_ = os.RemoveAll(tmpDir)
	os.Exit(code)
}

type testEnv struct {
	t         *testing.T
	rootDir   string
	configDir string
	stateDir  string
	homeDir   string
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	env := &testEnv{
		t:         t,
		rootDir:   root,
		configDir: filepath.Join(root, "config"),
		stateDir:  filepath.Join(root, "state"),
		homeDir:   filepath.Join(root, "home"),
	}
	for _, dir := range []string{env.configDir, env.stateDir, env.homeDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	return env
}

func (e *testEnv) environ() []string {
	overrides := map[string]string{
		"XDG_CONFIG_HOME": e.configDir,
		"XDG_STATE_HOME":  e.stateDir,
		"HOME":            e.homeDir,
		"USERPROFILE":     e.homeDir,
		"APPDATA":         e.configDir,
		"LOCALAPPDATA":    e.stateDir,
	}
	base := os.Environ()
	out := make([]string, 0, len(base)+len(overrides))
	for _, kv := range base {
		k, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if _, skip := overrides[strings.ToUpper(k)]; skip {
			continue
		}
		if _, skip := overrides[k]; skip {
			continue
		}
		out = append(out, kv)
	}
	for k, v := range overrides {
		out = append(out, k+"="+v)
	}
	return out
}

func (e *testEnv) mkRepo(relPath string) string {
	e.t.Helper()
	dir := filepath.Join(e.rootDir, "repos", relPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		e.t.Fatalf("mkdir repo %s: %v", dir, err)
	}
	return dir
}

func (e *testEnv) writeConfig(repoDir, content string) string {
	e.t.Helper()
	cfgPath := filepath.Join(repoDir, ".cron.toml")
	// Atomic write via temp+rename to mirror real editors (Vim/VS Code).
	tmpPath := cfgPath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(content), 0o644); err != nil {
		e.t.Fatalf("write temp config: %v", err)
	}
	if err := os.Rename(tmpPath, cfgPath); err != nil {
		e.t.Fatalf("rename config: %v", err)
	}
	return cfgPath
}

func (e *testEnv) run(workDir string, args ...string) (stdout, stderr string, err error) {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, dcronBin, args...)
	cmd.Dir = workDir
	cmd.Env = e.environ()
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return outBuf.String(), errBuf.String(), err
}

func (e *testEnv) mustRun(workDir string, args ...string) (stdout, stderr string) {
	e.t.Helper()
	stdout, stderr, err := e.run(workDir, args...)
	if err != nil {
		e.t.Fatalf("dcron %v failed: %v\nstdout:\n%s\nstderr:\n%s", args, err, stdout, stderr)
	}
	return stdout, stderr
}

func (e *testEnv) controlPath() string {
	return filepath.Join(e.stateDir, "dcron", "control.json")
}

func (e *testEnv) statePath() string {
	return filepath.Join(e.stateDir, "dcron", "state.json")
}

type daemonProc struct {
	cmd      *exec.Cmd
	stderr   *bytes.Buffer
	done     chan error
	stopOnce sync.Once
	stopErr  error
}

func (e *testEnv) startDaemon() *daemonProc {
	e.t.Helper()
	cmd := exec.Command(dcronBin, "daemon", "run")
	cmd.Dir = e.rootDir
	cmd.Env = e.environ()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		e.t.Fatalf("start dcron daemon run: %v", err)
	}
	proc := &daemonProc{
		cmd:    cmd,
		stderr: &stderr,
		done:   make(chan error, 1),
	}
	go func() {
		proc.done <- cmd.Wait()
	}()

	waitFor(e.t, 5*time.Second, func() bool {
		_, ctrlErr := os.Stat(e.controlPath())
		_, stateErr := os.Stat(e.statePath())
		return ctrlErr == nil && stateErr == nil
	}, "daemon control.json and state.json created")

	e.t.Cleanup(func() {
		_ = proc.stop()
	})
	return proc
}

func (p *daemonProc) stop() error {
	p.stopOnce.Do(func() {
		select {
		case err := <-p.done:
			p.stopErr = err
			return
		default:
		}

		if runtime.GOOS == "windows" {
			// Terminate the entire process tree on Windows so active child jobs
			// release directory handles before t.TempDir() cleanup runs.
			if err := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(p.cmd.Process.Pid)).Run(); err != nil {
				_ = p.cmd.Process.Kill()
			}
		} else {
			_ = p.cmd.Process.Signal(syscall.SIGTERM)
		}

		select {
		case err := <-p.done:
			p.stopErr = err
		case <-time.After(5 * time.Second):
			_ = p.cmd.Process.Kill()
			p.stopErr = <-p.done
		}
	})
	return p.stopErr
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, desc string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for: %s", timeout, desc)
}

func appendMarkerCmd(markerPath, text string) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf(`echo %s>>"%s"`, text, markerPath)
	}
	return fmt.Sprintf(`printf '%%s\n' %q >> %q`, text, markerPath)
}

func printEnvCmd() string {
	if runtime.GOOS == "windows" {
		return `echo %DCRON_REPO%^|%DCRON_JOB%^|%CUSTOM_FLAG%`
	}
	return `printf '%s|%s|%s\n' "$DCRON_REPO" "$DCRON_JOB" "$CUSTOM_FLAG"`
}

func sleepCmd(seconds int) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("ping -n %d 127.0.0.1 >nul", seconds+1)
	}
	return fmt.Sprintf("sleep %d", seconds)
}

func TestE2E_InitValidateRegisterListRunLogsUnregister(t *testing.T) {
	env := newTestEnv(t)
	repo := env.mkRepo("starter-app")

	// 1. dcron init creates a cross-platform starter .cron.toml and auto-registers the repo.
	out, _ := env.mustRun(repo, "init")
	if !strings.Contains(out, "Created") || !strings.Contains(out, `Registered "starter-app"`) {
		t.Fatalf("unexpected init output:\n%s", out)
	}

	// 2. dcron validate validates the generated starter template and previews upcoming runs.
	out, _ = env.mustRun(repo, "validate", "--next", "2")
	if !strings.Contains(out, "Valid:") || !strings.Contains(out, "[sync]") || !strings.Contains(out, "0 * * * *") {
		t.Fatalf("unexpected validate output:\n%s", out)
	}

	// 3. dcron list displays the registered repo and job.
	out, _ = env.mustRun(env.rootDir, "list")
	if !strings.Contains(out, "starter-app") || !strings.Contains(out, "sync") {
		t.Fatalf("unexpected list output:\n%s", out)
	}

	// 4. dcron run executes the starter job out-of-the-box on every OS.
	out, _ = env.mustRun(repo, "run", "sync")
	if !strings.Contains(out, "running sync") {
		t.Fatalf("expected starter job output 'running sync', got:\n%s", out)
	}

	// 5. dcron logs displays structured START/output/END entries.
	out, _ = env.mustRun(env.rootDir, "logs", "starter-app/sync")
	for _, want := range []string{"START repo=starter-app job=sync", "running sync", "END exit=0"} {
		if !strings.Contains(out, want) {
			t.Errorf("logs missing %q:\n%s", want, out)
		}
	}

	// 6. unregister and re-register by explicit path with custom alias.
	out, _ = env.mustRun(repo, "unregister")
	if !strings.Contains(out, `Unregistered "starter-app"`) {
		t.Fatalf("unexpected unregister output:\n%s", out)
	}

	out, _ = env.mustRun(env.rootDir, "register", repo, "--name", "custom-alias")
	if !strings.Contains(out, `Registered "custom-alias"`) {
		t.Fatalf("unexpected register output:\n%s", out)
	}
}

func TestE2E_DaemonLifecycleTriggerOverlapAndShutdown(t *testing.T) {
	env := newTestEnv(t)
	repo := env.mkRepo("svc-repo")

	cfg := fmt.Sprintf(`
[defaults]
overlap = "skip"
env = { CUSTOM_FLAG = "e2e-ok" }

[jobs.echo_env]
description = "Print injected dcron env vars"
schedule = "every hour"
command = %q

[jobs.slow_job]
description = "Blocks long enough to test overlap=skip"
schedule = "every hour"
command = %q
`, printEnvCmd(), sleepCmd(5))
	env.writeConfig(repo, cfg)
	env.mustRun(repo, "register")

	proc := env.startDaemon()

	// Verify `dcron status` sees the live daemon and loaded jobs.
	waitFor(t, 4*time.Second, func() bool {
		out, _, err := env.run(env.rootDir, "status")
		return err == nil && strings.Contains(out, "Daemon: running") && strings.Contains(out, "echo_env")
	}, "dcron status shows running daemon with echo_env")

	// Synchronous trigger via live daemon HTTP control server.
	out, _ := env.mustRun(env.rootDir, "trigger", "--wait", "svc-repo/echo_env")
	if !strings.Contains(out, "Triggered and completed svc-repo/echo_env in daemon") || !strings.Contains(out, "exit 0") {
		t.Fatalf("unexpected sync trigger output:\n%s", out)
	}

	// Verify job output and injected env vars were written to the job's log file.
	logsOut, _ := env.mustRun(env.rootDir, "logs", "svc-repo/echo_env")
	if !strings.Contains(logsOut, "svc-repo|echo_env|e2e-ok") {
		t.Fatalf("expected injected env output in logs, got:\n%s", logsOut)
	}

	// Verify `dcron status` reflects successful run count = 1.
	statusOut, _ := env.mustRun(env.rootDir, "status")
	if !strings.Contains(statusOut, "ok") {
		t.Fatalf("expected status 'ok' after trigger, got:\n%s", statusOut)
	}

	// Async trigger slow_job, then verify overlap=skip rejects a concurrent trigger.
	out, _ = env.mustRun(env.rootDir, "trigger", "svc-repo/slow_job")
	if !strings.Contains(out, "Triggered svc-repo/slow_job in daemon") {
		t.Fatalf("unexpected async trigger output:\n%s", out)
	}

	_, errOut, err := env.run(env.rootDir, "trigger", "svc-repo/slow_job")
	if err == nil {
		t.Fatalf("expected second concurrent trigger with overlap=skip to fail")
	}
	if !strings.Contains(errOut, "already running") {
		t.Fatalf("expected 'already running' error, got stderr:\n%s", errOut)
	}

	// Shut down daemon and verify cleanup.
	_ = proc.stop()
	if runtime.GOOS != "windows" {
		if _, err := os.Stat(env.controlPath()); !os.IsNotExist(err) {
			t.Errorf("expected control.json to be removed after graceful shutdown")
		}
	}
	statusOut, _ = env.mustRun(env.rootDir, "status")
	if !strings.Contains(statusOut, "Daemon: stopped") {
		t.Fatalf("expected Daemon: stopped after shutdown, got:\n%s", statusOut)
	}
}

func TestE2E_DaemonLiveAutoReloadAndValidateBeforeSwap(t *testing.T) {
	env := newTestEnv(t)
	repo := env.mkRepo("reload-repo")
	marker := filepath.Join(env.rootDir, "auto-reload-marker.txt")

	initialCfg := `
[jobs.hourly_job]
schedule = "every hour"
command = "echo initial"
`
	env.writeConfig(repo, initialCfg)
	env.mustRun(repo, "register")

	proc := env.startDaemon()
	defer func() { _ = proc.stop() }()

	waitFor(t, 4*time.Second, func() bool {
		out, _, err := env.run(env.rootDir, "status")
		return err == nil && strings.Contains(out, "hourly_job")
	}, "initial job loaded by daemon")

	// Live-edit .cron.toml via atomic rename to add a 1s interval job.
	updatedCfg := fmt.Sprintf(`
[jobs.fast_tick]
schedule = "every 1s"
command = %q
`, appendMarkerCmd(marker, "tick"))
	env.writeConfig(repo, updatedCfg)

	waitFor(t, 6*time.Second, func() bool {
		data, err := os.ReadFile(marker)
		return err == nil && strings.Contains(string(data), "tick")
	}, "auto-reloaded 1s job executed and wrote marker file")

	// Write a broken .cron.toml: validate-before-swap must surface <config error>
	// in `dcron status` while keeping fast_tick alive.
	env.writeConfig(repo, "[jobs.broken]\nschedule = \"not a valid schedule\"\ncommand = \"echo nope\"\n")

	waitFor(t, 4*time.Second, func() bool {
		out, errOut, err := env.run(env.rootDir, "status")
		return err == nil &&
			strings.Contains(out, "<config error>") &&
			strings.Contains(out, "fast_tick") &&
			strings.Contains(errOut, "unrecognized schedule")
	}, "validate-before-swap records config error while retaining fast_tick")

	// Fix .cron.toml and verify the error clears automatically.
	env.writeConfig(repo, `
[jobs.recovered]
schedule = "every hour"
command = "echo recovered"
`)

	waitFor(t, 4*time.Second, func() bool {
		out, _, err := env.run(env.rootDir, "status")
		return err == nil && strings.Contains(out, "recovered") && !strings.Contains(out, "<config error>")
	}, "fixed config clears config error on reload")
}

func TestE2E_DaemonRebootJobRunsOncePerOSBoot(t *testing.T) {
	env := newTestEnv(t)
	repo := env.mkRepo("boot-repo")
	marker := filepath.Join(env.rootDir, "boot-runs.txt")

	cfg := fmt.Sprintf(`
[jobs.warmup]
schedule = "on reboot"
command = %q
`, appendMarkerCmd(marker, "booted"))
	env.writeConfig(repo, cfg)
	env.mustRun(repo, "register")

	// First daemon startup: on-reboot job must execute once.
	proc1 := env.startDaemon()
	waitFor(t, 5*time.Second, func() bool {
		data, err := os.ReadFile(marker)
		return err == nil && len(strings.Fields(string(data))) == 1
	}, "on-reboot job executed once on first daemon start")

	waitFor(t, 4*time.Second, func() bool {
		out, _, err := env.run(env.rootDir, "status")
		return err == nil && strings.Contains(out, "ran this boot")
	}, "status reports 'ran this boot'")

	_ = proc1.stop()

	// Second daemon startup within the SAME OS boot: must NOT re-run the reboot job.
	proc2 := env.startDaemon()
	defer func() { _ = proc2.stop() }()

	// Give the daemon time to load all repos and settle.
	waitFor(t, 4*time.Second, func() bool {
		out, _, err := env.run(env.rootDir, "status")
		return err == nil && strings.Contains(out, "Daemon: running") && strings.Contains(out, "warmup")
	}, "second daemon loaded boot-repo")
	time.Sleep(400 * time.Millisecond)

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if runs := len(strings.Fields(string(data))); runs != 1 {
		t.Fatalf("expected on-reboot job to run exactly once per OS boot, got %d runs:\n%s", runs, string(data))
	}
}

func TestE2E_LocalTriggerFallbackTimeoutAndMultiRepo(t *testing.T) {
	env := newTestEnv(t)
	repoA := env.mkRepo(filepath.Join("work", "api"))
	repoB := env.mkRepo(filepath.Join("personal", "api"))

	cfgA := fmt.Sprintf(`
[jobs.ping]
schedule = "every hour"
command = "echo from-work-api"

[jobs.timeout_test]
schedule = "every hour"
timeout = "200ms"
command = %q
`, sleepCmd(10))
	env.writeConfig(repoA, cfgA)
	env.writeConfig(repoB, `
[jobs.ping]
schedule = "every hour"
command = "echo from-personal-api"
`)

	// Registering two directories named "api" auto-disambiguates to "api" and "api-2".
	outA, _ := env.mustRun(repoA, "register")
	outB, _ := env.mustRun(repoB, "register")
	if !strings.Contains(outA, `Registered "api"`) || !strings.Contains(outB, `Registered "api-2"`) {
		t.Fatalf("unexpected disambiguated register output:\nA: %s\nB: %s", outA, outB)
	}

	// Trigger with daemon stopped falls back to local execution and persists telemetry.
	out, errOut := env.mustRun(env.rootDir, "trigger", "api-2/ping")
	if !strings.Contains(out, "from-personal-api") || !strings.Contains(errOut, "Daemon is not running") {
		t.Fatalf("unexpected local fallback trigger output:\nout=%s\nerr=%s", out, errOut)
	}
	statusOut, _ := env.mustRun(env.rootDir, "status")
	if !strings.Contains(statusOut, "api-2") || !strings.Contains(statusOut, "ok") {
		t.Fatalf("expected status to record local fallback run:\n%s", statusOut)
	}

	// Run a job with a short timeout and verify fast cancellation and error message.
	start := time.Now()
	_, errOut, err := env.run(env.rootDir, "run", "api/timeout_test")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("expected timeout_test to fail with timeout error")
	}
	if !strings.Contains(errOut, "timed out") {
		t.Fatalf("expected 'timed out' in stderr, got:\n%s", errOut)
	}
	if elapsed > 4*time.Second {
		t.Fatalf("timeout cleanup took too long: %s", elapsed)
	}
}
