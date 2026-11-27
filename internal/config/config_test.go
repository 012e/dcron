package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/012e/dcron/internal/schedule"
)

func TestTOMLLoader_LoadValid(t *testing.T) {
	dir := t.TempDir()
	content := `
[defaults]
shell = "/bin/bash"
timeout = "5m"
overlap = "wait"
dir = "scripts"
env = { A = "1", B = "default" }

[jobs.sync]
description = "Hourly sync"
schedule = "every hour"
command = "./sync.sh"
env = { B = "override", C = "3" }

[jobs.boot]
schedule = "on reboot"
command = "make warmup"
enabled = false
timeout = "30s"
overlap = "skip"
dir = "."
`
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	loader := NewTOMLLoader(schedule.NewHybridParser())
	cfg, err := loader.Load(dir)
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	if len(cfg.Jobs) != 2 {
		t.Fatalf("got %d jobs, want 2", len(cfg.Jobs))
	}

	syncJob := cfg.Jobs["sync"]
	if !syncJob.Enabled {
		t.Error("sync job should default to enabled=true")
	}
	if syncJob.Timeout != 5*time.Minute {
		t.Errorf("sync timeout = %v, want 5m", syncJob.Timeout)
	}
	if syncJob.Overlap != OverlapWait {
		t.Errorf("sync overlap = %q, want %q", syncJob.Overlap, OverlapWait)
	}
	if syncJob.Shell != "/bin/bash" {
		t.Errorf("sync shell = %q, want /bin/bash", syncJob.Shell)
	}
	wantWorkDir := filepath.Join(dir, "scripts")
	if syncJob.WorkDir != wantWorkDir {
		t.Errorf("sync workdir = %q, want %q", syncJob.WorkDir, wantWorkDir)
	}
	if syncJob.Env["A"] != "1" || syncJob.Env["B"] != "override" || syncJob.Env["C"] != "3" {
		t.Errorf("unexpected merged env: %#v", syncJob.Env)
	}
	if syncJob.Schedule.Kind != schedule.KindCron || syncJob.Schedule.CronExpr != "0 * * * *" {
		t.Errorf("unexpected sync schedule: %+v", syncJob.Schedule)
	}

	bootJob := cfg.Jobs["boot"]
	if bootJob.Enabled {
		t.Error("boot job should be disabled")
	}
	if bootJob.Timeout != 30*time.Second {
		t.Errorf("boot timeout = %v, want 30s", bootJob.Timeout)
	}
	if bootJob.Overlap != OverlapSkip {
		t.Errorf("boot overlap = %q, want %q", bootJob.Overlap, OverlapSkip)
	}
	if bootJob.WorkDir != dir {
		t.Errorf("boot workdir = %q, want %q", bootJob.WorkDir, dir)
	}
	if bootJob.Schedule.Kind != schedule.KindReboot {
		t.Errorf("boot schedule kind = %v, want KindReboot", bootJob.Schedule.Kind)
	}
}

func TestTOMLLoader_ValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		toml    string
		wantErr string
	}{
		{
			name:    "unknown key typo",
			toml:    "[jobs.a]\nshedule = 'every hour'\ncommand = 'echo hi'\n",
			wantErr: "unknown configuration key",
		},
		{
			name:    "missing schedule",
			toml:    "[jobs.a]\ncommand = 'echo hi'\n",
			wantErr: "schedule is required",
		},
		{
			name:    "missing command",
			toml:    "[jobs.a]\nschedule = 'every hour'\n",
			wantErr: "command is required",
		},
		{
			name:    "invalid schedule",
			toml:    "[jobs.a]\nschedule = 'whenever'\ncommand = 'echo hi'\n",
			wantErr: "unrecognized schedule",
		},
		{
			name:    "invalid overlap",
			toml:    "[jobs.a]\nschedule = 'every hour'\ncommand = 'echo hi'\noverlap = 'crash'\n",
			wantErr: "invalid overlap policy",
		},
		{
			name:    "invalid timeout",
			toml:    "[jobs.a]\nschedule = 'every hour'\ncommand = 'echo hi'\ntimeout = '-5s'\n",
			wantErr: "invalid positive timeout",
		},
	}

	loader := NewTOMLLoader(schedule.NewHybridParser())
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, FileName), []byte(tc.toml), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := loader.Load(dir)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestStarterTemplateIsValid(t *testing.T) {
	dir := t.TempDir()
	tmpl := StarterTemplate()
	if strings.Contains(tmpl, "\nshell = \"/bin/bash\"") {
		t.Fatalf("StarterTemplate should not hardcode /bin/bash so it works cross-platform")
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(tmpl), 0o644); err != nil {
		t.Fatal(err)
	}
	loader := NewTOMLLoader(schedule.NewHybridParser())
	cfg, err := loader.Load(dir)
	if err != nil {
		t.Fatalf("StarterTemplate should be valid, got: %v", err)
	}
	if len(cfg.Jobs) == 0 {
		t.Fatal("StarterTemplate should define at least one example job")
	}
}

func TestResolveDefaultShell(t *testing.T) {
	winEnv := func(k string) string {
		switch k {
		case "SHELL":
			return "/usr/bin/bash" // e.g. Git Bash env var must not break native Windows default
		case "COMSPEC":
			return `C:\Windows\System32\cmd.exe`
		default:
			return ""
		}
	}
	if got := resolveDefaultShell("windows", winEnv); got != `C:\Windows\System32\cmd.exe` {
		t.Errorf("windows default shell = %q, want COMSPEC", got)
	}
	if got := resolveDefaultShell("windows", func(string) string { return "" }); got != "cmd.exe" {
		t.Errorf("windows fallback shell = %q, want cmd.exe", got)
	}
	if got := resolveDefaultShell("darwin", func(k string) string {
		if k == "SHELL" {
			return "/bin/zsh"
		}
		return ""
	}); got != "/bin/zsh" {
		t.Errorf("darwin default shell = %q, want /bin/zsh", got)
	}
	if got := resolveDefaultShell("linux", func(string) string { return "" }); got != "/bin/sh" {
		t.Errorf("linux fallback shell = %q, want /bin/sh", got)
	}
}

func TestTOMLLoader_LoadFileAndTildeExpansion(t *testing.T) {
	tmp := t.TempDir()
	fakeHome := filepath.Join(tmp, "home-user")
	if err := os.MkdirAll(fakeHome, 0o755); err != nil {
		t.Fatal(err)
	}
	absCustom := filepath.Join(tmp, "custom-abs")

	globalPath := filepath.Join(tmp, "config", GlobalFileName)
	if err := os.MkdirAll(filepath.Dir(globalPath), 0o755); err != nil {
		t.Fatal(err)
	}
	content := `
[defaults]
timeout = "2m"

[jobs.default_home]
schedule = "every hour"
command = "echo home"

[jobs.tilde_root]
schedule = "daily at 9am"
command = "echo tilde"
dir = "~"

[jobs.tilde_sub]
schedule = "every 10 minutes"
command = "echo sub"
dir = "~/scripts/cron"

[jobs.relative_dir]
schedule = "every 15 minutes"
command = "echo rel"
dir = "bin"

[jobs.abs_dir]
schedule = "every 30 minutes"
command = "echo abs"
dir = "` + filepath.ToSlash(absCustom) + `"
`
	if err := os.WriteFile(globalPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	loader := NewTOMLLoader(schedule.NewHybridParser())
	loader.homeDir = func() (string, error) { return fakeHome, nil }

	cfg, err := loader.LoadFile(globalPath, "")
	if err != nil {
		t.Fatalf("LoadFile() unexpected error: %v", err)
	}
	if cfg.ConfigPath != globalPath || cfg.RepoPath != globalPath {
		t.Errorf("ConfigPath=%q RepoPath=%q, want %q", cfg.ConfigPath, cfg.RepoPath, globalPath)
	}

	if got := cfg.Jobs["default_home"].WorkDir; got != fakeHome {
		t.Errorf("default_home WorkDir = %q, want %q", got, fakeHome)
	}
	if got := cfg.Jobs["tilde_root"].WorkDir; got != fakeHome {
		t.Errorf("tilde_root WorkDir = %q, want %q", got, fakeHome)
	}
	if got, want := cfg.Jobs["tilde_sub"].WorkDir, filepath.Join(fakeHome, "scripts", "cron"); got != want {
		t.Errorf("tilde_sub WorkDir = %q, want %q", got, want)
	}
	if got, want := cfg.Jobs["relative_dir"].WorkDir, filepath.Join(fakeHome, "bin"); got != want {
		t.Errorf("relative_dir WorkDir = %q, want %q", got, want)
	}
	if got, want := cfg.Jobs["abs_dir"].WorkDir, filepath.Clean(absCustom); got != want {
		t.Errorf("abs_dir WorkDir = %q, want %q", got, want)
	}

	// Verify tilde expansion also works in repo-local .cron.toml defaults.dir.
	repoDir := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	repoCfg := `
[defaults]
dir = "~/workspace"

[jobs.ping]
schedule = "every hour"
command = "echo ping"
`
	if err := os.WriteFile(filepath.Join(repoDir, FileName), []byte(repoCfg), 0o644); err != nil {
		t.Fatal(err)
	}
	rc, err := loader.Load(repoDir)
	if err != nil {
		t.Fatalf("Load() repo with ~ dir failed: %v", err)
	}
	if got, want := rc.Jobs["ping"].WorkDir, filepath.Join(fakeHome, "workspace"); got != want {
		t.Errorf("repo ping WorkDir = %q, want %q", got, want)
	}
}

func TestGlobalStarterTemplateIsValid(t *testing.T) {
	tmp := t.TempDir()
	globalPath := filepath.Join(tmp, GlobalFileName)
	tmpl := GlobalStarterTemplate()
	if strings.Contains(tmpl, "\nshell = \"/bin/bash\"") {
		t.Fatalf("GlobalStarterTemplate should not hardcode /bin/bash")
	}
	if err := os.WriteFile(globalPath, []byte(tmpl), 0o644); err != nil {
		t.Fatal(err)
	}
	loader := NewTOMLLoader(schedule.NewHybridParser())
	cfg, err := loader.LoadFile(globalPath, "")
	if err != nil {
		t.Fatalf("GlobalStarterTemplate should be valid, got: %v", err)
	}
	if len(cfg.Jobs) == 0 {
		t.Fatal("GlobalStarterTemplate should define at least one example job")
	}
}
