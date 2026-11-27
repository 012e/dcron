package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	TriggerJob(ctx context.Context, req TriggerRequest) (TriggerResponse, error)
	Snapshot() state.DaemonSnapshot
}

// Options configures a Service instance via explicit dependency injection.
type Options struct {
	Registry         registry.Store
	Loader           config.Loader
	Runner           runner.Runner
	Scheduler        Scheduler
	Watcher          watcher.Watcher
	StateStore       state.Store
	BootID           state.BootIDProvider
	GlobalConfigPath string
	LogDir           string
	ControlPath      string
	Logger           *slog.Logger
	PID              int
	Now              func() time.Time
}

// Service is the concrete Daemon implementation.
type Service struct {
	registry         registry.Store
	loader           config.Loader
	runner           runner.Runner
	scheduler        Scheduler
	watcher          watcher.Watcher
	stateStore       state.Store
	bootIDProv       state.BootIDProvider
	globalConfigPath string
	logDir           string
	controlPath      string
	logger           *slog.Logger
	pid              int
	now              func() time.Time

	mu          sync.Mutex
	runCtx      context.Context
	ready       bool
	bootID      string
	bootTime    time.Time
	snapshot    state.DaemonSnapshot
	watchedDirs map[string]string                // repoPath -> watched config path
	repoNames   map[string]string                // repoPath -> registry name
	handles     map[string]map[string]JobHandle  // repoPath -> jobName -> handle
	jobs        map[string]map[string]config.Job // repoPath -> jobName -> resolved job
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

	globalCfgPath := strings.TrimSpace(opts.GlobalConfigPath)
	if globalCfgPath != "" {
		if canon, err := registry.CanonicalPath(globalCfgPath); err == nil {
			globalCfgPath = canon
		}
	}

	return &Service{
		registry:         opts.Registry,
		loader:           opts.Loader,
		runner:           opts.Runner,
		scheduler:        opts.Scheduler,
		watcher:          opts.Watcher,
		stateStore:       opts.StateStore,
		bootIDProv:       opts.BootID,
		globalConfigPath: globalCfgPath,
		logDir:           opts.LogDir,
		controlPath:      opts.ControlPath,
		logger:           logger,
		pid:              pid,
		now:              now,
		runCtx:           context.Background(),
		watchedDirs:      make(map[string]string),
		repoNames:        make(map[string]string),
		handles:          make(map[string]map[string]JobHandle),
		jobs:             make(map[string]map[string]config.Job),
		snapshot: state.DaemonSnapshot{
			Repos: make(map[string]state.RepoState),
		},
	}, nil
}

// Run initializes state, loads all registered repos, starts the scheduler, and watches
// for registry and .cron.toml changes until ctx is cancelled.
func (s *Service) Run(ctx context.Context) error {
	s.mu.Lock()
	s.runCtx = ctx
	s.mu.Unlock()

	if err := s.initState(); err != nil {
		return err
	}

	var ctrl *controlServer
	if s.controlPath != "" {
		var err error
		ctrl, err = startControlServer(s.controlPath, s)
		if err != nil {
			s.logger.Warn("failed to start control server", "path", s.controlPath, "err", err)
		} else {
			s.logger.Info("dcron control server listening", "path", s.controlPath)
		}
	}

	regPath := s.registry.Path()
	if canon, err := registry.CanonicalPath(regPath); err == nil {
		regPath = canon
	}

	if s.watcher != nil {
		if err := os.MkdirAll(filepath.Dir(regPath), 0o755); err != nil {
			s.logger.Warn("failed to create registry directory", "path", filepath.Dir(regPath), "err", err)
		} else if err := s.watcher.WatchFile(regPath); err != nil {
			s.logger.Warn("failed to watch registry file", "path", regPath, "err", err)
		}
		if s.globalConfigPath != "" {
			if err := os.MkdirAll(filepath.Dir(s.globalConfigPath), 0o755); err != nil {
				s.logger.Warn("failed to create global config directory", "path", filepath.Dir(s.globalConfigPath), "err", err)
			} else if err := s.watcher.WatchFile(s.globalConfigPath); err != nil {
				s.logger.Warn("failed to watch global config file", "path", s.globalConfigPath, "err", err)
			}
		}
	}

	if err := s.ReloadAll(ctx); err != nil {
		s.logger.Warn("initial load completed with errors", "err", err)
	}

	s.scheduler.Start()
	s.mu.Lock()
	s.ready = true
	s.mu.Unlock()
	s.logger.Info("dcron daemon started", "pid", s.pid, "boot_id", s.bootID)

	defer func() {
		s.mu.Lock()
		s.ready = false
		s.mu.Unlock()
		if ctrl != nil {
			ctrl.Close()
		}
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

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-s.watcher.Events():
			if !ok {
				return nil
			}
			if samePath(ev.Path, regPath) {
				s.logger.Info("registry changed, reloading all repos")
				if err := s.ReloadAll(ctx); err != nil {
					s.logger.Warn("reload after registry change had errors", "err", err)
				}
				continue
			}
			if s.isGlobalPath(ev.Path) {
				s.logger.Info("global config changed, reloading global jobs", "path", s.globalConfigPath)
				if err := s.ReloadGlobal(ctx); err != nil {
					s.logger.Warn("global config reload failed (keeping previous schedule)", "path", s.globalConfigPath, "err", err)
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
	var bootTime time.Time
	if s.bootIDProv != nil {
		if id, err := s.bootIDProv.BootID(); err == nil {
			bootID = id
		} else {
			s.logger.Debug("boot ID unavailable", "err", err)
		}
		if btp, ok := s.bootIDProv.(state.BootTimeProvider); ok {
			if bt, err := btp.BootTime(); err == nil {
				bootTime = bt.UTC()
			} else {
				s.logger.Debug("boot time unavailable", "err", err)
			}
		}
	}

	now := s.now().UTC()
	s.mu.Lock()
	s.bootID = bootID
	s.bootTime = bootTime
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

	// Remove unregistered repos first (never treat the global config as an unregistered repo).
	s.mu.Lock()
	var removed []string
	for rp := range s.repoNames {
		if s.isGlobalPath(rp) {
			continue
		}
		if _, ok := active[rp]; !ok {
			removed = append(removed, rp)
		}
	}
	for rp := range s.snapshot.Repos {
		if s.isGlobalPath(rp) {
			continue
		}
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

	if s.globalConfigPath != "" {
		if err := s.ReloadGlobal(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	return firstErr
}

func (s *Service) isGlobalPath(p string) bool {
	if s.globalConfigPath == "" || strings.TrimSpace(p) == "" {
		return false
	}
	gp := s.globalConfigPath
	if canonGP, err := registry.CanonicalPath(gp); err == nil {
		gp = canonGP
	}
	if samePath(p, s.globalConfigPath) || samePath(p, gp) {
		return true
	}
	if canon, err := registry.CanonicalPath(p); err == nil {
		return samePath(canon, s.globalConfigPath) || samePath(canon, gp)
	}
	return false
}

// ReloadGlobal loads and validates the user-wide global.toml file if it exists, or unloads
// global jobs cleanly if the file has been removed.
func (s *Service) ReloadGlobal(_ context.Context) error {
	if s.globalConfigPath == "" {
		return nil
	}
	if _, err := os.Stat(s.globalConfigPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.unloadGlobal()
			return nil
		}
	}

	s.mu.Lock()
	s.repoNames[s.globalConfigPath] = config.GlobalRepoName
	s.mu.Unlock()

	cfg, err := s.loader.LoadFile(s.globalConfigPath, "")
	if errors.Is(err, os.ErrNotExist) {
		s.unloadGlobal()
		return nil
	}
	return s.applyLoadedConfig(s.globalConfigPath, config.GlobalRepoName, cfg, err)
}

func (s *Service) unloadGlobal() {
	if s.globalConfigPath == "" {
		return
	}
	tag := repoTag(s.globalConfigPath)
	_ = s.scheduler.RemoveByTag(tag)

	s.mu.Lock()
	_, hadName := s.repoNames[s.globalConfigPath]
	_, hadHandles := s.handles[s.globalConfigPath]
	_, hadJobs := s.jobs[s.globalConfigPath]
	_, hadSnap := s.snapshot.Repos[s.globalConfigPath]
	if !hadName && !hadHandles && !hadJobs && !hadSnap {
		s.mu.Unlock()
		return
	}
	delete(s.repoNames, s.globalConfigPath)
	delete(s.handles, s.globalConfigPath)
	delete(s.jobs, s.globalConfigPath)
	delete(s.snapshot.Repos, s.globalConfigPath)
	s.snapshot.UpdatedAt = s.now().UTC()
	snapCopy := s.snapshot.Clone()
	s.mu.Unlock()

	_ = s.stateStore.Save(snapCopy)
}

func (s *Service) unloadRepo(repoPath string) {
	tag := repoTag(repoPath)
	_ = s.scheduler.RemoveByTag(tag)

	s.mu.Lock()
	cfgPath, wasWatched := s.watchedDirs[repoPath]
	delete(s.watchedDirs, repoPath)
	delete(s.repoNames, repoPath)
	delete(s.handles, repoPath)
	delete(s.jobs, repoPath)
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
func (s *Service) ReloadRepo(ctx context.Context, repoPath string) error {
	canon, err := registry.CanonicalPath(repoPath)
	if err == nil {
		repoPath = canon
	}
	if s.isGlobalPath(repoPath) {
		return s.ReloadGlobal(ctx)
	}

	s.mu.Lock()
	repoName := s.repoNames[repoPath]
	if repoName == "" {
		repoName = filepath.Base(repoPath)
	}
	s.mu.Unlock()

	cfg, err := s.loader.Load(repoPath)
	return s.applyLoadedConfig(repoPath, repoName, cfg, err)
}

func (s *Service) applyLoadedConfig(repoPath, repoName string, cfg *config.RepoConfig, loadErr error) error {
	if loadErr != nil {
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
		rs.ConfigError = loadErr.Error()
		s.snapshot.Repos[repoPath] = rs
		s.snapshot.UpdatedAt = s.now().UTC()
		snapCopy := s.snapshot.Clone()
		s.mu.Unlock()

		_ = s.stateStore.Save(snapCopy)
		return loadErr
	}

	tag := repoTag(repoPath)
	if err := s.scheduler.RemoveByTag(tag); err != nil {
		return fmt.Errorf("remove existing jobs for %s: %w", repoPath, err)
	}

	s.mu.Lock()
	prevRepo := s.snapshot.Repos[repoPath]
	bootID := s.bootID
	bootRef := s.bootTime
	if bootRef.IsZero() {
		bootRef = s.snapshot.StartedAt
	}
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
				var runAt time.Time
				if job.Schedule.Kind == schedule.KindReboot && job.Schedule.RebootDelay > 0 {
					ref := bootRef
					if ref.IsZero() {
						ref = s.now().UTC()
					}
					runAt = ref.Add(job.Schedule.RebootDelay)
				}
				handle, schedErr := s.scheduler.ScheduleJob(JobSpec{
					Tag:      tag,
					Name:     fmt.Sprintf("%s/%s", repoName, job.Name),
					Schedule: job.Schedule,
					Overlap:  job.Overlap,
					RunAt:    runAt,
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
					} else if !runAt.IsZero() && runAt.After(s.now().UTC()) {
						n := runAt.UTC()
						js.NextRun = &n
					}
				}
			}
		}

		newJobs[job.Name] = js
	}

	now := s.now().UTC()
	resolvedJobs := make(map[string]config.Job, len(cfg.Jobs))
	for name, job := range cfg.Jobs {
		resolvedJobs[name] = job
	}

	s.mu.Lock()
	s.handles[repoPath] = newHandles
	s.jobs[repoPath] = resolvedJobs
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

// TriggerJob manually executes a configured job in the daemon, updating persisted state
// and writing output to the job's log file.
func (s *Service) TriggerJob(ctx context.Context, req TriggerRequest) (TriggerResponse, error) {
	if req.JobName == "" {
		return TriggerResponse{}, fmt.Errorf("job name is required")
	}
	if req.RepoPath == "" {
		return TriggerResponse{}, fmt.Errorf("repo path is required")
	}

	repoPath := req.RepoPath
	if canon, err := registry.CanonicalPath(repoPath); err == nil {
		repoPath = canon
	}

	s.mu.Lock()
	repoName := s.repoNames[repoPath]
	job, hasJob := s.jobs[repoPath][req.JobName]
	var alreadyRunning bool
	if rs, ok := s.snapshot.Repos[repoPath]; ok {
		if js, ok := rs.Jobs[req.JobName]; ok {
			alreadyRunning = js.Running
		}
	}
	daemonCtx := s.runCtx
	s.mu.Unlock()

	if !hasJob {
		// Fall back to loading config directly if the repo or global config has not been reloaded yet.
		var (
			cfg *config.RepoConfig
			err error
		)
		if s.isGlobalPath(repoPath) {
			cfg, err = s.loader.LoadFile(repoPath, "")
			repoName = config.GlobalRepoName
		} else {
			cfg, err = s.loader.Load(repoPath)
		}
		if err != nil {
			return TriggerResponse{}, fmt.Errorf("load config for %s: %w", repoPath, err)
		}
		j, ok := cfg.Jobs[req.JobName]
		if !ok {
			return TriggerResponse{}, fmt.Errorf("job %q not found in %s (available: %v)", req.JobName, repoPath, cfg.SortedJobNames())
		}
		job = j
		if repoName == "" {
			if entry, ok, err := s.registry.Find(repoPath); err == nil && ok {
				repoName = entry.Name
			} else {
				repoName = filepath.Base(repoPath)
			}
		}
	}
	if repoName == "" {
		if s.isGlobalPath(repoPath) {
			repoName = config.GlobalRepoName
		} else {
			repoName = filepath.Base(repoPath)
		}
	}

	if alreadyRunning && (job.Overlap == config.OverlapSkip || job.Overlap == "") {
		return TriggerResponse{
			RepoName: repoName,
			JobName:  job.Name,
			Error:    fmt.Sprintf("job %s/%s is already running (overlap=skip)", repoName, job.Name),
		}, fmt.Errorf("job %s/%s is already running (overlap=skip)", repoName, job.Name)
	}

	logPath := ""
	if s.logDir != "" {
		logPath = runner.LogPathFor(s.logDir, repoName, repoPath, job.Name)
	}

	s.logger.Info("manual trigger requested", "repo", repoName, "job", job.Name, "wait", req.Wait)

	if req.Wait {
		res := s.executeJob(ctx, repoName, repoPath, job, logPath)
		resp := TriggerResponse{
			RepoName:   repoName,
			JobName:    job.Name,
			Completed:  true,
			ExitCode:   res.ExitCode,
			DurationMs: res.Duration.Milliseconds(),
		}
		if res.Err != nil {
			resp.Error = res.Err.Error()
		}
		return resp, nil
	}

	if daemonCtx == nil {
		daemonCtx = context.Background()
	}
	started := make(chan struct{})
	go func() {
		s.executeJobWithHook(daemonCtx, repoName, repoPath, job, logPath, func() {
			close(started)
		})
	}()
	<-started

	return TriggerResponse{
		RepoName:  repoName,
		JobName:   job.Name,
		Completed: false,
	}, nil
}

func (s *Service) executeJob(ctx context.Context, repoName, repoPath string, job config.Job, logPath string) runner.Result {
	return s.executeJobWithHook(ctx, repoName, repoPath, job, logPath, nil)
}

func (s *Service) executeJobWithHook(ctx context.Context, repoName, repoPath string, job config.Job, logPath string, onStarted func()) runner.Result {
	s.logger.Info("starting job", "repo", repoName, "job", job.Name)

	s.mu.Lock()
	bootID := s.bootID
	if rs, ok := s.snapshot.Repos[repoPath]; ok {
		if js, ok := rs.Jobs[job.Name]; ok {
			js.Running = true
			if job.Schedule.Kind == schedule.KindReboot {
				js.NextRun = nil
				if bootID != "" {
					js.LastBootID = bootID
				}
			}
			rs.Jobs[job.Name] = js
			s.snapshot.Repos[repoPath] = rs
			s.snapshot.UpdatedAt = s.now().UTC()
		}
	}
	startSnap := s.snapshot.Clone()
	s.mu.Unlock()
	_ = s.stateStore.Save(startSnap)

	if onStarted != nil {
		onStarted()
	}

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
			if job.Schedule.Kind == schedule.KindReboot {
				js.NextRun = nil
				if bootID != "" {
					js.LastBootID = bootID
				}
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
	return res
}

// Snapshot returns a copy of the current in-memory daemon snapshot.
func (s *Service) Snapshot() state.DaemonSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot.Clone()
}

func (s *Service) isReady() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ready
}

func repoTag(repoPath string) string {
	return "repo:" + repoPath
}

func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}
