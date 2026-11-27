package watcher

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Event represents a debounced change notification for a watched target file.
type Event struct {
	Path string
}

// Watcher monitors specific files (by watching their parent directories so atomic
// renames and editor swaps are caught reliably) and emits debounced change events.
type Watcher interface {
	WatchFile(filePath string) error
	UnwatchFile(filePath string) error
	Events() <-chan Event
	Errors() <-chan error
	Close() error
}

// FSNotifyWatcher implements Watcher using fsnotify with per-file debouncing.
type FSNotifyWatcher struct {
	fsw      *fsnotify.Watcher
	debounce time.Duration

	mu     sync.Mutex
	dirs   map[string]map[string]string // dir -> basename -> full target path
	timers map[string]*time.Timer
	closed bool

	events chan Event
	errors chan error
	done   chan struct{}
}

// NewFSNotifyWatcher constructs an FSNotifyWatcher with the given debounce duration.
// If debounce <= 0, it defaults to 250ms.
func NewFSNotifyWatcher(debounce time.Duration) (*FSNotifyWatcher, error) {
	if debounce <= 0 {
		debounce = 250 * time.Millisecond
	}
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create fsnotify watcher: %w", err)
	}

	w := &FSNotifyWatcher{
		fsw:      fsw,
		debounce: debounce,
		dirs:     make(map[string]map[string]string),
		timers:   make(map[string]*time.Timer),
		events:   make(chan Event, 64),
		errors:   make(chan error, 16),
		done:     make(chan struct{}),
	}
	go w.loop()
	return w, nil
}

// WatchFile registers filePath for change notifications by watching its parent directory.
func (w *FSNotifyWatcher) WatchFile(filePath string) error {
	abs, err := filepath.Abs(filePath)
	if err != nil {
		return fmt.Errorf("resolve watch path %q: %w", filePath, err)
	}
	dir := resolveWatchDir(filepath.Dir(abs))
	base := filepath.Base(abs)
	dirKey := watchKey(dir)
	baseKey := watchKey(base)

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return fmt.Errorf("watcher is closed")
	}

	files, exists := w.dirs[dirKey]
	if !exists {
		if err := w.fsw.Add(dir); err != nil {
			return fmt.Errorf("watch directory %s: %w", dir, err)
		}
		files = make(map[string]string)
		w.dirs[dirKey] = files
	}
	files[baseKey] = abs
	return nil
}

// UnwatchFile stops reporting changes for filePath and removes the directory watch
// if no other files in that directory are watched.
func (w *FSNotifyWatcher) UnwatchFile(filePath string) error {
	abs, err := filepath.Abs(filePath)
	if err != nil {
		return fmt.Errorf("resolve unwatch path %q: %w", filePath, err)
	}
	dir := resolveWatchDir(filepath.Dir(abs))
	base := filepath.Base(abs)
	dirKey := watchKey(dir)
	baseKey := watchKey(base)

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}

	if t, ok := w.timers[watchKey(abs)]; ok {
		t.Stop()
		delete(w.timers, watchKey(abs))
	}

	files, exists := w.dirs[dirKey]
	if !exists {
		return nil
	}
	delete(files, baseKey)
	if len(files) == 0 {
		delete(w.dirs, dirKey)
		_ = w.fsw.Remove(dir)
	}
	return nil
}

func (w *FSNotifyWatcher) Events() <-chan Event {
	return w.events
}

func (w *FSNotifyWatcher) Errors() <-chan error {
	return w.errors
}

// Close shuts down the underlying fsnotify watcher and stops all pending timers.
func (w *FSNotifyWatcher) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	for k, t := range w.timers {
		t.Stop()
		delete(w.timers, k)
	}
	w.mu.Unlock()

	err := w.fsw.Close()
	<-w.done
	return err
}

func (w *FSNotifyWatcher) loop() {
	defer func() {
		w.mu.Lock()
		w.closed = true
		for k, t := range w.timers {
			t.Stop()
			delete(w.timers, k)
		}
		close(w.events)
		close(w.errors)
		w.mu.Unlock()
		close(w.done)
	}()

	for {
		select {
		case ev, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			// Ignore pure chmod noise; care about write/create/remove/rename.
			if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
				continue
			}
			w.handleRawEvent(ev.Name)

		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			select {
			case w.errors <- err:
			default:
			}
		}
	}
}

func (w *FSNotifyWatcher) handleRawEvent(name string) {
	clean := filepath.Clean(name)
	dirKey := watchKey(resolveWatchDir(filepath.Dir(clean)))
	baseKey := watchKey(filepath.Base(clean))

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}

	files, ok := w.dirs[dirKey]
	if !ok {
		return
	}
	target, ok := files[baseKey]
	if !ok {
		return
	}

	timerKey := watchKey(target)
	if existing, ok := w.timers[timerKey]; ok {
		existing.Reset(w.debounce)
		return
	}

	w.timers[timerKey] = time.AfterFunc(w.debounce, func() {
		w.mu.Lock()
		if w.closed {
			w.mu.Unlock()
			return
		}
		delete(w.timers, timerKey)
		// Send while holding mu (non-blocking) so Close() cannot close w.events concurrently.
		select {
		case w.events <- Event{Path: target}:
		default:
		}
		w.mu.Unlock()
	})
}

func resolveWatchDir(dir string) string {
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(dir)
}

func watchKey(p string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(p)
	}
	return p
}
