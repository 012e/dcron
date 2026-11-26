package runner

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/012e/dcron/internal/config"
)

func TestExecRunner_SuccessAndEnvAndLogs(t *testing.T) {
	dir := t.TempDir()
	logPath := LogPathFor(filepath.Join(dir, "logs"), "my-repo", dir, "hello")

	var stdout bytes.Buffer
	r := NewExecRunner()
	res := r.Run(context.Background(), Request{
		RepoName: "my-repo",
		RepoPath: dir,
		LogPath:  logPath,
		Stdout:   &stdout,
		Job: config.Job{
			Name:    "hello",
			Shell:   "/bin/sh",
			WorkDir: dir,
			Command: `printf "%s|%s|%s" "$DCRON_REPO" "$DCRON_JOB" "$CUSTOM_VAR"`,
			Env:     map[string]string{"CUSTOM_VAR": "world"},
		},
	})

	if res.Err != nil {
		t.Fatalf("Run() unexpected error: %v", res.Err)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}
	if stdout.String() != "my-repo|hello|world" {
		t.Errorf("stdout = %q, want %q", stdout.String(), "my-repo|hello|world")
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
	r := NewExecRunner()
	res := r.Run(context.Background(), Request{
		RepoName: "repo",
		RepoPath: dir,
		Job: config.Job{
			Name:    "fail",
			Shell:   "/bin/sh",
			WorkDir: dir,
			Command: "exit 42",
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
	r := NewExecRunner()
	res := r.Run(context.Background(), Request{
		RepoName: "repo",
		RepoPath: dir,
		Job: config.Job{
			Name:    "slow",
			Shell:   "/bin/sh",
			WorkDir: dir,
			Command: "sleep 10",
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

func TestLogPathFor_NoCollision(t *testing.T) {
	p1 := LogPathFor("/logs", "app", "/home/u/work/app", "sync")
	p2 := LogPathFor("/logs", "app", "/home/u/personal/app", "sync")
	if p1 == p2 {
		t.Fatalf("expected distinct log paths for different repos, both got %q", p1)
	}
}
