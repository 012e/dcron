package watcher

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFSNotifyWatcher_ModifyAndAtomicRenameAndDebounce(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, ".cron.toml")
	other := filepath.Join(dir, "README.md")

	w, err := NewFSNotifyWatcher(50 * time.Millisecond)
	if err != nil {
		t.Fatalf("NewFSNotifyWatcher: %v", err)
	}
	defer func() { _ = w.Close() }()

	if err := w.WatchFile(target); err != nil {
		t.Fatalf("WatchFile: %v", err)
	}

	// Editing an unrelated file should NOT trigger an event.
	if err := os.WriteFile(other, []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-w.Events():
		t.Fatalf("unexpected event for unrelated file: %+v", ev)
	case <-time.After(120 * time.Millisecond):
	}

	// Rapid burst of writes should coalesce into one debounced event.
	for i := range 5 {
		if err := os.WriteFile(target, []byte("v="+string(rune('0'+i))), 0o644); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}

	select {
	case ev := <-w.Events():
		if ev.Path != target {
			t.Errorf("ev.Path = %q, want %q", ev.Path, target)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for modify event")
	}

	// Ensure no duplicate burst event leaked through.
	select {
	case ev := <-w.Events():
		t.Fatalf("expected debounced single event, got extra: %+v", ev)
	case <-time.After(120 * time.Millisecond):
	}

	// Atomic write + rename (Vim/VS Code/git style) should still be caught!
	tmp := filepath.Join(dir, ".cron.toml.tmp")
	if err := os.WriteFile(tmp, []byte("atomic"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, target); err != nil {
		t.Fatal(err)
	}

	select {
	case ev := <-w.Events():
		if ev.Path != target {
			t.Errorf("ev.Path = %q, want %q", ev.Path, target)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for atomic rename event")
	}

	// Unwatching stops further events.
	if err := w.UnwatchFile(target); err != nil {
		t.Fatalf("UnwatchFile: %v", err)
	}
	if err := os.WriteFile(target, []byte("after unwatch"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-w.Events():
		t.Fatalf("unexpected event after UnwatchFile: %+v", ev)
	case <-time.After(120 * time.Millisecond):
	}
}
