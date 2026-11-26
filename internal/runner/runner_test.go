package runner

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/012e/dcron/internal/config"
)

func testShellAndCommands() (shell, envCmd, failCmd, slowCmd string) {
	if runtime.GOOS == "windows" {
		return "cmd.exe",
			`echo %DCRON_REPO%^|%DCRON_JOB%^|%CUSTOM_VAR%`,
			`exit /B 42`,
			`ping -n 15 127.0.0.1 >nul`
	}
	return "/bin/sh",
		`printf "%s|%s|%s" "$DCRON_REPO" "$DCRON_JOB" "$CUSTOM_VAR"`,
		`exit 42`,
		`sleep 10`
}

func TestExecRunner_SuccessAndEnvAndLogs(t *testing.T) {
	dir := t.TempDir()
	logPath := LogPathFor(filepath.Join(dir, "logs"), "my-repo", dir, "hello")
	shell, envCmd, _, _ := testShellAndCommands()

	var stdout bytes.Buffer
	r := NewExecRunner()
	res := r.Run(context.Background(), Request{
		RepoName: "my-repo",
		RepoPath: dir,
		LogPath:  logPath,
		Stdout:   &stdout,
		Job: config.Job{
			Name:    "hello",
			Shell:   shell,
			WorkDir: dir,
			Command: envCmd,
			Env:     map[string]string{"CUSTOM_VAR": "world"},
		},
	})

	if res.Err != nil {
		t.Fatalf("Run() unexpected error: %v", res.Err)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}
	if got := strings.TrimSpace(stdout.String()); got != "my-repo|hello|world" {
		t.Errorf("stdout = %q, want %q", got, "my-repo|hello|world")
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	logStr := string(logBytes)
	if !strings.Contains(logStr, "START repo=my-repo job=hello") || !strings.Contains(logStr, "my-repo|hello|world") || !strings.Contains(logStr, "END exit=0") {
		t.Errorf("unexpected log contents:\n%s", logStr)
	}
}

func TestExecRunner_NonZeroExit(t *testing.T) {
	dir := t.TempDir()
	shell, _, failCmd, _ := testShellAndCommands()

	r := NewExecRunner()
	res := r.Run(context.Background(), Request{
		RepoName: "repo",
		RepoPath: dir,
		Job: config.Job{
			Name:    "fail",
			Shell:   shell,
			WorkDir: dir,
			Command: failCmd,
		},
	})

	if res.Err == nil {
		t.Fatal("expected error for exit 42")
	}
	if res.ExitCode != 42 {
		t.Errorf("ExitCode = %d, want 42", res.ExitCode)
	}
}

func TestExecRunner_Timeout(t *testing.T) {
	dir := t.TempDir()
	shell, _, _, slowCmd := testShellAndCommands()

	r := NewExecRunner()
	res := r.Run(context.Background(), Request{
		RepoName: "repo",
		RepoPath: dir,
		Job: config.Job{
			Name:    "slow",
			Shell:   shell,
			WorkDir: dir,
			Command: slowCmd,
			Timeout: 150 * time.Millisecond,
		},
	})

	if res.Err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(res.Err.Error(), "timed out") {
		t.Errorf("expected timeout error message, got: %v", res.Err)
	}
	if res.Duration > 3*time.Second {
		t.Errorf("timeout took too long: %v", res.Duration)
	}
}

func TestMergeEnv_CaseInsensitiveWindowsOverrides(t *testing.T) {
	base := []string{
		"Path=C:\\Windows\\System32",
		"=C:=C:\\Users\\test",
		"FOO=bar",
	}
	overrides := map[string]string{
		"PATH":       "C:\\custom\\bin",
		"DCRON_REPO": "demo",
	}

	got := mergeEnv(base, overrides, true)
	want := []string{
		"DCRON_REPO=demo",
		"FOO=bar",
		"PATH=C:\\custom\\bin",
	}
	if !slices.Equal(got, want) {
		t.Errorf("mergeEnv(caseInsensitive=true) = %v, want %v", got, want)
	}

	// Case-sensitive (Unix) keeps both Path and PATH.
	gotUnix := mergeEnv([]string{"Path=/a", "FOO=bar"}, map[string]string{"PATH": "/b"}, false)
	wantUnix := []string{"FOO=bar", "PATH=/b", "Path=/a"}
	if !slices.Equal(gotUnix, wantUnix) {
		t.Errorf("mergeEnv(caseInsensitive=false) = %v, want %v", gotUnix, wantUnix)
	}
}

func TestLogPathFor_NoCollision(t *testing.T) {
	p1 := LogPathFor("/logs", "app", "/home/u/work/app", "sync")
	p2 := LogPathFor("/logs", "app", "/home/u/personal/app", "sync")
	if p1 == p2 {
		t.Fatalf("expected distinct log paths for different repos, both got %q", p1)
	}
}
