package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileStore_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(filepath.Join(dir, "state.json"))

	// Missing file returns empty snapshot.
	empty, err := store.Load()
	if err != nil {
		t.Fatalf("Load() missing: %v", err)
	}
	if len(empty.Repos) != 0 {
		t.Fatalf("expected empty repos, got %d", len(empty.Repos))
	}

	now := time.Now().UTC().Truncate(time.Second)
	code := 0
	snap := DaemonSnapshot{
		PID:       12345,
		StartedAt: now,
		UpdatedAt: now,
		BootID:    "boot-abc",
		Repos: map[string]RepoState{
			"/tmp/repo": {
				Name:     "repo",
				Path:     "/tmp/repo",
				LoadedAt: now,
				Jobs: map[string]JobState{
					"sync": {
						Name:            "sync",
						ScheduleRaw:     "every hour",
						ScheduleSummary: "0 * * * *",
						ScheduleKind:    "cron",
						Enabled:         true,
						LastRun:         &now,
						LastExitCode:    &code,
						RunCount:        3,
					},
				},
			},
		},
	}

	if err := store.Save(snap); err != nil {
		t.Fatalf("Save(): %v", err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if loaded.PID != 12345 || loaded.BootID != "boot-abc" {
		t.Errorf("unexpected header: %+v", loaded)
	}
	js := loaded.Repos["/tmp/repo"].Jobs["sync"]
	if js.RunCount != 3 || js.LastExitCode == nil || *js.LastExitCode != 0 {
		t.Errorf("unexpected job state: %+v", js)
	}
}

func TestLinuxBootIDProvider(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "boot_id")
	if err := os.WriteFile(p, []byte("550e8400-e29b-41d4-a716-446655440000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	up := filepath.Join(dir, "uptime")
	if err := os.WriteFile(up, []byte("120.50 450.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixedNow := time.Date(2026, 11, 26, 10, 5, 0, 0, time.UTC)
	prov := &LinuxBootIDProvider{
		Path:       p,
		UptimePath: up,
		Now:        func() time.Time { return fixedNow },
	}
	id, err := prov.BootID()
	if err != nil {
		t.Fatalf("BootID(): %v", err)
	}
	if id != "550e8400-e29b-41d4-a716-446655440000" {
		t.Errorf("BootID() = %q", id)
	}
	bt, err := prov.BootTime()
	if err != nil {
		t.Fatalf("BootTime(): %v", err)
	}
	wantBT := fixedNow.Add(-120500 * time.Millisecond)
	if !bt.Equal(wantBT) {
		t.Errorf("BootTime() = %v, want %v", bt, wantBT)
	}
}

func TestOSProcessChecker(t *testing.T) {
	checker := OSProcessChecker{}
	if !checker.IsAlive(os.Getpid()) {
		t.Error("current process should be alive")
	}
	if checker.IsAlive(-1) || checker.IsAlive(0) {
		t.Error("non-positive PID should not be alive")
	}
}
