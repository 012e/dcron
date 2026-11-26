package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/012e/dcron/internal/config"
	"github.com/012e/dcron/internal/registry"
	"github.com/012e/dcron/internal/runner"
	"github.com/012e/dcron/internal/schedule"
	"github.com/012e/dcron/internal/state"
	"github.com/012e/dcron/internal/watcher"
)

// Daemon manages watching registered repos, scheduling jobs, and persisting telemetry.
type Daemon interface {
	Run(ctx context.Context) error
	ReloadAll(ctx context.Context) error
	ReloadRepo(ctx context.Context, repoPath string) error
	Snapshot() state.DaemonSnapshot
}

// Options configures a Service instance via explicit dependency injection.
type Options struct {
	Registry   registry.Store
	Loader     config.Loader
	Runner     runner.Runner
	Scheduler  Scheduler
	Watcher    watcher.Watcher
	StateStore state.Store
	BootID     state.BootIDProvider
	LogDir     string
	Logger     *slog.Logger
	PID        int
	Now        func() time.Time
}

// Service is the concrete Daemon implementation.
type Service struct {
	registry   registry.Store
	loader     config.Loader
	runner     runner.Runner
	scheduler  Scheduler
	watcher    watcher.Watcher
	stateStore state.Store
	bootIDProv state.BootIDProvider
	logDir     string
	logger     *slog.Logger
	pid        int
	now        func() time.Time

	mu          sync.Mutex
	bootID      string
	snapshot    state.DaemonSnapshot
	watchedDirs map[string]string // repoPath -> watched config path
	repoNames   map[string]string // repoPath -> registry name
	handles     map[string]map[string]JobHandle
}

// NewService creates a new Service with the provided dependencies.
func NewService(opts Options) (*Service, error) {
	if opts.Registry == nil || opts.Loader == nil || opts.Runner == nil || opts.Scheduler == nil || opts.StateStore == nil {
		return nil, fmt.Errorf("registry, loader, runner, scheduler, and state store are required")
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	pid := opts.PID
	if pid == 0 {
		pid = os.Getpid()
	}

	return &Service{
		registry:    opts.Registry,
		loader:      opts.Loader,
		runner:      opts.Runner,
		scheduler:   opts.Scheduler,
		watcher:     opts.Watcher,
		stateStore:  opts.StateStore,
		bootIDProv:  opts.BootID,
		logDir:      opts.LogDir,
		logger:      logger,
		pid:         pid,
		now:         now,
		watchedDirs: make(map[string]string),
		repoNames:   make(map[string]string),
		handles:     make(map[string]map[string]JobHandle),
		snapshot: state.DaemonSnapshot{
			Repos: make(map[string]state.RepoState),
		},
	}, nil
}

// Run initializes state, loads all registered repos, starts the scheduler, and watches
// for registry and .cron.toml changes until ctx is cancelled.
func (s *Service) Run(ctx context.Context) error {
	if err := s.initState(); err != nil {
		return err
	}

	if s.watcher != nil {
		regPath := s.registry.Path()
		if err := os.MkdirAll(filepath.Dir(regPath), 0o755); err != nil {
			s.logger.Warn("failed to create registry directory", "path", filepath.Dir(regPath), "err", err)
		} else if err := s.watcher.WatchFile(regPath); err != nil {
			s.logger.Warn("failed to watch registry file", "path", regPath, "err", err)
		}
	}

	if err := s.ReloadAll(ctx); err != nil {
		s.logger.Warn("initial load completed with errors", "err", err)
	}

	s.scheduler.Start()
	s.logger.Info("dcron daemon started", "pid", s.pid, "boot_id", s.bootID)

	defer func() {
		if s.watcher != nil {
			_ = s.watcher.Close()
		}
		if err := s.scheduler.Shutdown(); err != nil {
			s.logger.Warn("scheduler shutdown error", "err", err)
		}
		s.markStopped()
		s.logger.Info("dcron daemon stopped")
	}()

	if s.watcher == nil {
		<-ctx.Done()
		return nil
	}

	regPath, _ := filepath.Abs(s.registry.Path())
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-s.watcher.Events():
			if !ok {
				return nil
			}
			if ev.Path == regPath {
				s.logger.Info("registry changed, reloading all repos")
				if err := s.ReloadAll(ctx); err != nil {
					s.logger.Warn("reload after registry change had errors", "err", err)
				}
				continue
			}
			repoPath := filepath.Dir(ev.Path)
			s.logger.Info("config changed, reloading repo", "repo", repoPath)
			if err := s.ReloadRepo(ctx, repoPath); err != nil {
				s.logger.Warn("repo reload failed (keeping previous schedule)", "repo", repoPath, "err", err)
			}
		case err, ok := <-s.watcher.Errors():
			if !ok {
				return nil
			}
			s.logger.Warn("file watcher error", "err", err)
		}
	}
}

func (s *Service) initState() error {
	prev, err := s.stateStore.Load()
	if err != nil {
		s.logger.Warn("failed to load previous state, starting fresh", "err", err)
		prev = state.DaemonSnapshot{Repos: make(map[string]state.RepoState)}
	}

	var bootID string
	if s.bootIDProv != nil {
		if id, err := s.bootIDProv.BootID(); err == nil {
			bootID = id
		} else {
			s.logger.Debug("boot ID unavailable", "err", err)
		}
	}

	now := s.now().UTC()
	s.mu.Lock()
	s.bootID = bootID
	s.snapshot = prev
	if s.snapshot.Repos == nil {
		s.snapshot.Repos = make(map[string]state.RepoState)
	}
	// Reset stale running flags from any previous unclean shutdown.
	for rp, rs := range s.snapshot.Repos {
		for jn, js := range rs.Jobs {
			js.Running = false
			rs.Jobs[jn] = js
		}
		s.snapshot.Repos[rp] = rs
	}
	s.snapshot.PID = s.pid
	s.snapshot.StartedAt = now
	s.snapshot.UpdatedAt = now
	s.snapshot.BootID = bootID
	snapCopy := s.snapshot.Clone()
	s.mu.Unlock()

	return s.stateStore.Save(snapCopy)
}

func (s *Service) markStopped() {
	s.mu.Lock()
	s.snapshot.PID = 0
	s.snapshot.UpdatedAt = s.now().UTC()
	for rp, rs := range s.snapshot.Repos {
		for jn, js := range rs.Jobs {
			js.Running = false
			js.NextRun = nil
			rs.Jobs[jn] = js
		}
		s.snapshot.Repos[rp] = rs
	}
	snapCopy := s.snapshot.Clone()
	s.mu.Unlock()
	_ = s.stateStore.Save(snapCopy)
}

// ReloadAll reconciles watched repositories and scheduled jobs against the registry.
func (s *Service) ReloadAll(ctx context.Context) error {
	entries, err := s.registry.List()
	if err != nil {
		return fmt.Errorf("list registry: %w", err)
	}

	active := make(map[string]registry.Entry, len(entries))
	for _, e := range entries {
		active[e.Path] = e
	}

	// Remove unregistered repos first.
	s.mu.Lock()
	var removed []string
	for rp := range s.repoNames {
		if _, ok := active[rp]; !ok {
			removed = append(removed, rp)
		}
	}
	for rp := range s.snapshot.Repos {
		if _, ok := active[rp]; !ok {
			removed = append(removed, rp)
		}
	}
	s.mu.Unlock()

	seenRemoved := make(map[string]struct{}, len(removed))
	for _, rp := range removed {
		if _, dup := seenRemoved[rp]; dup {
			continue
		}
		seenRemoved[rp] = struct{}{}
		s.unloadRepo(rp)
	}

	// Register/update watches and load each active repo.
	var firstErr error
	for _, e := range entries {
		s.mu.Lock()
		s.repoNames[e.Path] = e.Name
		cfgPath := s.loader.ConfigPath(e.Path)
		_, alreadyWatched := s.watchedDirs[e.Path]
		s.mu.Unlock()

		if s.watcher != nil && !alreadyWatched {
			if err := s.watcher.WatchFile(cfgPath); err != nil {
				s.logger.Warn("failed to watch repo config", "repo", e.Name, "path", cfgPath, "err", err)
			} else {
				s.mu.Lock()
				s.watchedDirs[e.Path] = cfgPath
				s.mu.Unlock()
			}
		}

		if err := s.ReloadRepo(ctx, e.Path); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	return firstErr
}

func (s *Service) unloadRepo(repoPath string) {
	tag := repoTag(repoPath)
	_ = s.scheduler.RemoveByTag(tag)

	s.mu.Lock()
	cfgPath, wasWatched := s.watchedDirs[repoPath]
	delete(s.watchedDirs, repoPath)
	delete(s.repoNames, repoPath)
	delete(s.handles, repoPath)
	delete(s.snapshot.Repos, repoPath)
	s.snapshot.UpdatedAt = s.now().UTC()
	snapCopy := s.snapshot.Clone()
	s.mu.Unlock()

	if s.watcher != nil && wasWatched {
		_ = s.watcher.UnwatchFile(cfgPath)
	}
	_ = s.stateStore.Save(snapCopy)
}

// ReloadRepo loads and validates a repository's .cron.toml and atomically swaps its
// scheduled jobs. If validation fails, previously scheduled jobs remain intact and the
// error is recorded in state for `dcron status`.
func (s *Service) ReloadRepo(_ context.Context, repoPath string) error {
	canon, err := registry.CanonicalPath(repoPath)
	if err == nil {
		repoPath = canon
	}

	s.mu.Lock()
	repoName := s.repoNames[repoPath]
	if repoName == "" {
		repoName = filepath.Base(repoPath)
	}
	s.mu.Unlock()

	cfg, err := s.loader.Load(repoPath)
	if err != nil {
		// Validate-before-swap: keep existing jobs running, surface error in state.
		s.mu.Lock()
		rs, ok := s.snapshot.Repos[repoPath]
		if !ok {
			rs = state.RepoState{
				Name: repoName,
				Path: repoPath,
				Jobs: make(map[string]state.JobState),
			}
		}
		rs.Name = repoName
		rs.ConfigError = err.Error()
		s.snapshot.Repos[repoPath] = rs
		s.snapshot.UpdatedAt = s.now().UTC()
		snapCopy := s.snapshot.Clone()
		s.mu.Unlock()

		_ = s.stateStore.Save(snapCopy)
		return err
	}

	tag := repoTag(repoPath)
	if err := s.scheduler.RemoveByTag(tag); err != nil {
		return fmt.Errorf("remove existing jobs for %s: %w", repoPath, err)
	}

	s.mu.Lock()
	prevRepo := s.snapshot.Repos[repoPath]
	bootID := s.bootID
	s.mu.Unlock()

	newHandles := make(map[string]JobHandle, len(cfg.Jobs))
	newJobs := make(map[string]state.JobState, len(cfg.Jobs))

	for _, name := range cfg.SortedJobNames() {
		job := cfg.Jobs[name]
		prevJob := prevRepo.Jobs[name]

		logPath := ""
		if s.logDir != "" {
			logPath = runner.LogPathFor(s.logDir, repoName, repoPath, job.Name)
		}

		js := state.JobState{
			Name:            job.Name,
			Description:     job.Description,
			ScheduleRaw:     job.Schedule.Raw,
			ScheduleSummary: job.Schedule.Summary(),
			ScheduleKind:    job.Schedule.Kind.String(),
			Enabled:         job.Enabled,
			LastRun:         prevJob.LastRun,
			LastDurationMs:  prevJob.LastDurationMs,
			LastExitCode:    prevJob.LastExitCode,
			LastError:       prevJob.LastError,
			RunCount:        prevJob.RunCount,
			Running:         prevJob.Running,
			LastBootID:      prevJob.LastBootID,
			LogPath:         logPath,
		}

		if job.Enabled {
			shouldSchedule := true
			if job.Schedule.Kind == schedule.KindReboot && bootID != "" && prevJob.LastBootID == bootID {
				// Already executed during this OS boot; don't re-fire on config reload or daemon restart.
				shouldSchedule = false
			}

			if shouldSchedule {
				jobCopy := job
				rName := repoName
				rPath := repoPath
				lPath := logPath
				handle, schedErr := s.scheduler.ScheduleJob(JobSpec{
					Tag:      tag,
					Name:     fmt.Sprintf("%s/%s", repoName, job.Name),
					Schedule: job.Schedule,
					Overlap:  job.Overlap,
				}, func(runCtx context.Context) {
					s.executeJob(runCtx, rName, rPath, jobCopy, lPath)
				})
				if schedErr != nil {
					js.LastError = schedErr.Error()
					s.logger.Error("failed to schedule job", "repo", repoName, "job", job.Name, "err", schedErr)
				} else {
					newHandles[job.Name] = handle
					if job.Schedule.Kind != schedule.KindReboot {
						if next, err := handle.NextRun(); err == nil && !next.IsZero() {
							n := next.UTC()
							js.NextRun = &n
						}
					}
				}
			}
		}

		newJobs[job.Name] = js
	}

	now := s.now().UTC()
	s.mu.Lock()
	s.handles[repoPath] = newHandles
	s.snapshot.Repos[repoPath] = state.RepoState{
		Name:        repoName,
		Path:        repoPath,
		LoadedAt:    now,
		ConfigError: "",
		Jobs:        newJobs,
	}
	s.snapshot.UpdatedAt = now
	snapCopy := s.snapshot.Clone()
	s.mu.Unlock()

	return s.stateStore.Save(snapCopy)
}

func (s *Service) executeJob(ctx context.Context, repoName, repoPath string, job config.Job, logPath string) {
	s.logger.Info("starting job", "repo", repoName, "job", job.Name)

	s.mu.Lock()
	bootID := s.bootID
	if rs, ok := s.snapshot.Repos[repoPath]; ok {
		if js, ok := rs.Jobs[job.Name]; ok {
			js.Running = true
			if job.Schedule.Kind == schedule.KindReboot && bootID != "" {
				js.LastBootID = bootID
			}
			rs.Jobs[job.Name] = js
			s.snapshot.Repos[repoPath] = rs
			s.snapshot.UpdatedAt = s.now().UTC()
		}
	}
	startSnap := s.snapshot.Clone()
	s.mu.Unlock()
	_ = s.stateStore.Save(startSnap)

	res := s.runner.Run(ctx, runner.Request{
		RepoName: repoName,
		RepoPath: repoPath,
		Job:      job,
		LogPath:  logPath,
	})

	s.mu.Lock()
	if rs, ok := s.snapshot.Repos[repoPath]; ok {
		if js, ok := rs.Jobs[job.Name]; ok {
			js.Running = false
			started := res.StartedAt
			js.LastRun = &started
			js.LastDurationMs = res.Duration.Milliseconds()
			exitCode := res.ExitCode
			js.LastExitCode = &exitCode
			if res.Err != nil {
				js.LastError = res.Err.Error()
			} else {
				js.LastError = ""
			}
			js.RunCount++
			if job.Schedule.Kind == schedule.KindReboot && bootID != "" {
				js.LastBootID = bootID
			}
			if handle, ok := s.handles[repoPath][job.Name]; ok && job.Schedule.Kind != schedule.KindReboot {
				if next, err := handle.NextRun(); err == nil && !next.IsZero() {
					n := next.UTC()
					js.NextRun = &n
				}
			}
			rs.Jobs[job.Name] = js
			s.snapshot.Repos[repoPath] = rs
			s.snapshot.UpdatedAt = s.now().UTC()
		}
	}
	endSnap := s.snapshot.Clone()
	s.mu.Unlock()
	_ = s.stateStore.Save(endSnap)

	if res.Err != nil {
		s.logger.Warn("job failed", "repo", repoName, "job", job.Name, "exit", res.ExitCode, "duration", res.Duration, "err", res.Err)
	} else {
		s.logger.Info("job completed", "repo", repoName, "job", job.Name, "duration", res.Duration)
	}
}

// Snapshot returns a copy of the current in-memory daemon snapshot.
func (s *Service) Snapshot() state.DaemonSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot.Clone()
}

func repoTag(repoPath string) string {
	return "repo:" + repoPath
}
