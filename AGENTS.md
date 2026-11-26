# AGENTS.md

Guidance for AI coding agents working in `dcron` (`github.com/012e/dcron`).

## Project Overview

`dcron` is a Go CLI tool, background scheduler daemon, and interactive Bubble Tea TUI for managing repo-local cron jobs defined in `.cron.toml`.

- **Language / Toolchain**: Go (`go.mod` targets `go 1.27.4`).
- **Entrypoints**: Root `main.go` (enables `go install .`) and `cmd/dcron/main.go`. Both simply invoke `internal/cli.Execute()`.
- **Core Dependencies**:
  - `spf13/cobra` — CLI commands and flags
  - `BurntSushi/toml` — `.cron.toml` and `registry.toml` parsing/encoding
  - `robfig/cron/v3` — 5-field cron validation and next-run calculation
  - `go-co-op/gocron/v2` — background job scheduling and overlap policies
  - `fsnotify/fsnotify` — directory-level file watching for live reloads
  - `charmbracelet/{bubbletea,bubbles,lipgloss}` — interactive TUI dashboard
  - `golang.org/x/sys` — Windows process liveness and uptime/boot-time APIs

## Common Commands

```bash
# Run full test suite with race detector
go test -race ./...

# Run static analysis
go vet ./...

# Build all packages / install binary
go build ./...
go install .

# Verify cross-platform compilation when touching OS-specific files
GOOS=linux GOARCH=amd64 go build ./...
GOOS=darwin GOARCH=amd64 go build ./...
GOOS=windows GOARCH=amd64 go build ./...
```

## Architecture & Package Map

All application code lives under `internal/`:

- **`internal/cli`** — Cobra command definitions wired through `App` dependency injection (`root.go`) with production construction in `exec.go`.
  - Subcommands: `init`, `register`, `unregister`, `list`, `status`, `validate`, `run`, `trigger`, `logs`, `daemon` (`run`, `install`, `uninstall`, `status`), and `tui` (alias `ui`).
  - Target shorthand: commands taking a job (`run`, `trigger`, `logs`) accept either `<job>` (resolved via `--repo` or cwd) or `<repo>/<job>`.
- **`internal/config`** — Loads and validates `<repo>/.cron.toml` (`TOMLLoader`). Inherits `[defaults]` (`shell`, `timeout`, `overlap`, `dir`, `env`) into `[jobs.<name>]` and rejects unknown TOML keys via `md.Undecoded()`.
- **`internal/schedule`** — `HybridParser` parsing 5-field cron, `@descriptors`, `@every <dur>`, natural-language schedules (`every hour`, `daily at 9am`, `every weekday at 8:30am`, `monthly on the 1st at 00:00`, etc.), and reboot triggers (`on reboot`, `5 minutes after reboot`, `@reboot + 5m`).
- **`internal/registry`** — Persistent list of registered repositories in `registry.toml` (`FileStore`). Resolves canonical symlink-free paths (`CanonicalPath`), auto-deduplicates repo aliases, and persists atomically.
- **`internal/state`** — Daemon telemetry snapshot (`state.json`, `DaemonSnapshot`, `FileStore`), OS boot identification/boot time (`bootid_{linux,darwin,windows,other}.go`), and process liveness (`process_{unix,windows}.go`).
- **`internal/runner`** — Executes jobs in configured shell/workdir/env (`ExecRunner`), injects `DCRON_REPO`, `DCRON_REPO_PATH`, and `DCRON_JOB`, writes structured append-only per-job logs (`LogPathFor`), and cleans up child process groups/trees on timeout (`proc_{unix,windows}.go`).
- **`internal/watcher`** — `FSNotifyWatcher` wrapping `fsnotify`. Watches parent directories (not individual files) to handle atomic editor saves and `git pull`, with per-file debouncing (250ms default).
- **`internal/daemon`** —
  - `daemon.go`: `Service` orchestrating registry/config watching, repo reload, boot-aware reboot execution, manual triggers, and state persistence.
  - `scheduler.go`: `GoCronScheduler` adapting `gocron/v2` and mapping `skip | wait | allow` overlap policies.
  - `control.go`: Loopback HTTP control server (`127.0.0.1:0`) and client (`HTTPControlClient`) authenticated via a mode-`0600` bearer token in `control.json` (`/health`, `/trigger`).
  - `systemd.go`: Linux `systemd --user` unit manager (`SystemdManager`) with injectable `CommandExecutor`.
  - `launchd.go`: macOS `launchd` LaunchAgent manager (`LaunchdManager`) writing `~/Library/LaunchAgents/com.012e.dcron.plist`.
  - `taskscheduler.go`: Windows Task Scheduler manager (`WindowsTaskManager`) writing `<stateDir>\dcron-task.xml` and invoking `schtasks`.
  - `service_{linux,darwin,windows,other}.go`: Platform factory `NewOSServiceManager(executor)`.
- **`internal/tui`** — Bubble Tea dashboard (`model.go`, `view.go`, `data.go`, `styles.go`, `run.go`). Combines registry, `.cron.toml`, and `state.json` into an immutable `Snapshot`, supports filtering, log tailing/follow, editing `.cron.toml`, triggering jobs (via daemon with local fallback), and managing the systemd service.

## On-Disk State & Config Paths

- **Registry**: `$XDG_CONFIG_HOME/dcron/registry.toml` (or `~/.config/dcron/registry.toml`; `%AppData%\dcron\registry.toml` on Windows).
- **State directory**: `$XDG_STATE_HOME/dcron` (or `~/.local/state/dcron`; `%LOCALAPPDATA%\dcron` on Windows), containing:
  - `state.json` — persisted `state.DaemonSnapshot`
  - `control.json` — mode `0600` loopback address + bearer token while daemon is running
  - `logs/<repoSlug>-<hash>/<jobSlug>.log` — append-only per-job logs

## Key Design Invariants & Conventions

1. **Interface-based Dependency Injection**: Keep dependencies behind the package interfaces (`registry.Store`, `config.Loader`, `schedule.Parser`, `runner.Runner`, `state.Store`, `state.BootIDProvider`, `state.ProcessChecker`, `watcher.Watcher`, `daemon.Scheduler`, `daemon.Controller`, `daemon.ServiceManager`, `daemon.CommandExecutor`) and inject clocks (`Now func() time.Time`), `WorkDir`, and `Out`/`Err` writers. Do not hardcode OS paths or global singletons inside core logic.
2. **Validate-Before-Swap Reloads**: In `daemon.Service.ReloadRepo`, if loading or validating `.cron.toml` fails, previously scheduled jobs for that repo must remain active and the error must be stored in `RepoState.ConfigError`.
3. **Atomic File Writes**: All persistent files (`registry.toml`, `state.json`, `control.json`) are written via temp file in the target directory + `os.Chmod` + `os.Rename`. Preserve this pattern for any new persisted files.
4. **Boot-Aware `KindReboot` Jobs**:
   - Record `LastBootID` when a reboot job starts/finishes and skip rescheduling if `bootID != "" && prevJob.LastBootID == bootID`, so config reloads and daemon restarts do not re-run reboot jobs within the same OS boot.
   - Delayed reboot schedules (`RebootDelay > 0`) compute `RunAt` relative to OS `BootTime` (when `BootTimeProvider` is implemented), falling back to daemon `StartedAt` / current time.
5. **Daemon Ownership of `state.json`**: Local CLI runs (e.g., `dcron trigger --local` or fallback when stopped) must not overwrite `state.json` if `ProcessChecker.IsAlive(snap.PID)` is true.
6. **Strict `.cron.toml` Schema**: `config.TOMLLoader.Load` rejects unknown keys. When adding a config field, update `Defaults`/`RawJob`/`Job`, wire inheritance/validation, update `README.md` if user-facing, and add unit tests in `internal/config/config_test.go`.
7. **Cross-Platform Safety**:
   - Keep Unix and Windows process handling separated via `//go:build !windows` and `//go:build windows` files.
   - Keep boot ID providers separated via `bootid_{linux,darwin,windows,other}.go` and OS service managers via `service_{linux,darwin,windows,other}.go`.
   - `ServiceManager` is constructed via `daemon.NewOSServiceManager` (Linux `systemd --user`, macOS `launchd`, Windows Task Scheduler) and may be `nil` on other platforms (`service_other.go`); always guard against `nil`.
8. **Deterministic Output**: Sort map keys (repo names, job names via `SortedJobNames()`, environment variables) before rendering tables, writing files, or building subprocess environments.

## Testing Guidelines

- Every `internal/*` package has unit tests; add or update tests alongside behavior changes.
- Always isolate filesystem state with `t.TempDir()` and injected stores/fakes—never touch the user's real `~/.config/dcron`, `~/.local/state/dcron`, or `systemctl`.
- Use fixed `Now` functions for time-sensitive schedule, boot-delay, and TUI formatting tests.
- Run `go test -race ./... && go vet ./...` before concluding any code change.
