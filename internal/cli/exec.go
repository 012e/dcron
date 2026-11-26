package cli

import (
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/012e/dcron/internal/config"
	"github.com/012e/dcron/internal/daemon"
	"github.com/012e/dcron/internal/registry"
	"github.com/012e/dcron/internal/runner"
	"github.com/012e/dcron/internal/schedule"
	"github.com/012e/dcron/internal/state"
	"github.com/012e/dcron/internal/watcher"
)

// Execute wires the default production dependencies and executes the root dcron command.
func Execute() error {
	regPath, err := registry.DefaultPath()
	if err != nil {
		return err
	}
	stateDir, err := state.DefaultStateDir()
	if err != nil {
		return err
	}

	parser := schedule.NewHybridParser()
	loader := config.NewTOMLLoader(parser)
	regStore := registry.NewFileStore(regPath)
	stateStore := state.NewFileStore(filepath.Join(stateDir, "state.json"))
	logDir := filepath.Join(stateDir, "logs")
	execRunner := runner.NewExecRunner()
	svcMgr, _ := daemon.NewSystemdManager(daemon.OSCommandExecutor{})

	app := &App{
		Registry:       regStore,
		Loader:         loader,
		Parser:         parser,
		Runner:         execRunner,
		StateStore:     stateStore,
		ProcessChecker: state.OSProcessChecker{},
		ServiceManager: svcMgr,
		LogDir:         logDir,
		Out:            os.Stdout,
		Err:            os.Stderr,
		DaemonFactory: func() (daemon.Daemon, error) {
			sched, err := daemon.NewGoCronScheduler(time.Local)
			if err != nil {
				return nil, err
			}
			w, err := watcher.NewFSNotifyWatcher(250 * time.Millisecond)
			if err != nil {
				return nil, err
			}
			logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
				Level: slog.LevelInfo,
			}))
			return daemon.NewService(daemon.Options{
				Registry:   regStore,
				Loader:     loader,
				Runner:     execRunner,
				Scheduler:  sched,
				Watcher:    w,
				StateStore: stateStore,
				BootID:     state.NewLinuxBootIDProvider(),
				LogDir:     logDir,
				Logger:     logger,
			})
		},
	}

	return NewRootCmd(app).Execute()
}
