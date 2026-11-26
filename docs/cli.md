# CLI Reference

`dcron` provides subcommands for initializing and validating repo-local `.cron.toml` files, managing the repository registry, running or triggering jobs, tailing logs, controlling the background scheduler daemon, and launching the interactive TUI.

## General Conventions

### Target Shorthand (`<job|repo/job>`)

Commands that operate on a single job (`run`, `trigger`, `logs`) accept either:

- **`<job>`** — Looks up the job in the repository specified by `--repo`/`-r`, or in the current working directory if `--repo` is omitted.
- **`<repo>/<job>`** — Shorthand specifying both the registered repository alias (or path) and the job name (used when `--repo` is not set).

### Repository Resolution

When a repository reference is supplied (via positional `[path]`, `--repo`, or `<repo>/<job>`):

1. `dcron` first checks the registry by alias name or canonical path.
2. If not found in the registry, it resolves the argument as a filesystem path (canonicalized with symlinks resolved).
3. When omitted, `dcron` uses the current working directory.

### Injected Job Environment Variables

Whenever `dcron` executes a job (via the daemon, `dcron run`, `dcron trigger`, or the TUI), it merges the host environment, `[defaults].env`, and `[jobs.<name>].env`, and injects:

| Variable | Description |
|---|---|
| `DCRON_REPO` | Registered repository alias (or directory basename if unregistered) |
| `DCRON_REPO_PATH` | Canonical absolute path to the repository |
| `DCRON_JOB` | Job name from `[jobs.<name>]` |

Explicit `[jobs.<name>].env` entries override `[defaults].env` and injected defaults.

---

## Repository & Configuration Commands

### `dcron init [path]`

Creates a commented starter `.cron.toml` in `[path]` (defaults to the current directory) and registers the repository with `dcron`.

```bash
dcron init
dcron init ./my-repo --name myrepo
dcron init --force
```

| Flag | Short | Default | Description |
|---|---|---|---|
| `--force` | `-f` | `false` | Overwrite an existing `.cron.toml` |
| `--name` | `-n` | `""` | Custom alias for the repository (defaults to directory basename; auto-suffixed `-2`, `-3`, … on collisions) |

If the repository is already registered, `dcron init` keeps the existing registration and prints its alias.

---

### `dcron register [path]`

Validates `<path>/.cron.toml` and adds the repository to `registry.toml`. Defaults to the current directory.

```bash
dcron register
dcron register /path/to/repo --name backend
```

| Flag | Short | Default | Description |
|---|---|---|---|
| `--name` | `-n` | `""` | Custom alias for the repository (defaults to directory basename) |

Registration fails if `.cron.toml` is missing or invalid, if the canonical path is already registered, or if the chosen alias is already in use. When the daemon is running, it watches `registry.toml` and automatically picks up newly registered repositories.

---

### `dcron unregister [path-or-name]`

Removes a repository from `registry.toml` by alias or path (defaults to the current directory). Does **not** delete `.cron.toml` or existing log files.

```bash
dcron unregister
dcron unregister backend
dcron unregister /path/to/repo
```

When the daemon is running, it automatically unschedules and unwatches unregistered repositories.

---

### `dcron validate [path]`

Parses and validates `<path>/.cron.toml` (defaults to the current directory), prints resolved settings (`schedule`, `command`, `workdir`, `timeout`), and previews upcoming trigger times.

```bash
dcron validate
dcron validate ./my-repo --next 5
```

| Flag | Default | Description |
|---|---|---|
| `--next` | `3` | Number of upcoming run times to preview per non-reboot job (`0` disables preview) |

Unknown TOML keys, invalid durations, invalid overlap policies (`skip`, `wait`, `allow`), and invalid schedule expressions fail validation with a descriptive error.

---

## Inspection Commands

### `dcron list`

Lists all registered repositories and their configured jobs directly from each repository's `.cron.toml`.

```bash
dcron list
```

Columns: `REPO`, `JOB`, `SCHEDULE` (raw expression), `NORMALIZED` (parsed summary), `ENABLED`, and `COMMAND`. If a repository's `.cron.toml` currently has a parse/validation error, it appears as an `<error>` row without blocking other repositories.

---

### `dcron status`

Displays whether the background daemon is running (with PID and uptime) along with a table of registered repositories and runtime telemetry from `state.json`.

```bash
dcron status
```

Columns: `REPO`, `JOB`, `SCHEDULE`, `STATE`, `LAST RUN`, `NEXT RUN`, and `RUNS`.

Possible `STATE` values:

- `running` — Currently executing
- `ok` — Last execution exited with code `0`
- `failed (exit N)` — Last execution failed or exited non-zero
- `disabled` — `enabled = false` in `.cron.toml`
- `idle` — Enabled, no recorded runs yet
- `pending` — Registered repo has not yet been loaded by the daemon
- `error` — Repository has a `.cron.toml` error (details are printed to stderr; previously scheduled jobs continue running)

For `on reboot` jobs, `NEXT RUN` displays `ran this boot` once executed during the current OS boot, a timestamp if a delayed reboot run is pending while the daemon is alive, or `on next boot`.

---

### `dcron logs <job|repo/job>`

Displays recent output from a job's append-only log file, optionally streaming new output as it arrives.

```bash
dcron logs sync
dcron logs backend/sync -n 50
dcron logs sync -f
dcron logs sync --repo /path/to/repo -n 0
```

| Flag | Short | Default | Description |
|---|---|---|---|
| `--repo` | `-r` | `""` | Repository name or path (defaults to current directory) |
| `--lines` | `-n` | `100` | Number of trailing lines to show (`0` prints the entire file) |
| `--follow` | `-f` | `false` | Continuously stream appended log output (polls every 250ms until interrupted) |

Each run is delimited in the log file by structured headers:

```text
=== [2026-01-15T09:00:00Z] START repo=myrepo job=sync cmd="./scripts/sync.sh" ===
...command stdout/stderr...
=== [2026-01-15T09:00:02Z] END exit=0 duration=2.012s ===
```

---

## Job Execution Commands

### `dcron run <job|repo/job>`

Executes a job immediately in the foreground, streaming stdout and stderr directly to your terminal while also appending to the job's log file.

```bash
dcron run sync
dcron run backend/sync
dcron run sync --repo ./backend
```

| Flag | Short | Default | Description |
|---|---|---|---|
| `--repo` | `-r` | `""` | Repository name or path (defaults to current directory) |

`dcron run` is intended for quick foreground testing: it always runs locally, ignores daemon overlap limits, and does **not** mutate daemon telemetry in `state.json`.

---

### `dcron trigger <job|repo/job>`

Manually triggers a job through the running daemon's local control endpoint so execution respects the job's overlap policy, updates `state.json` telemetry, and runs in the daemon context. If the daemon is stopped (or `--local` is passed), `dcron` falls back to executing the job locally and recording telemetry when no daemon is alive.

```bash
# Fire-and-forget via running daemon
dcron trigger sync

# Trigger via daemon and wait for completion
dcron trigger --wait backend/sync

# Force local execution even if daemon is running
dcron trigger --local sync
```

| Flag | Short | Default | Description |
|---|---|---|---|
| `--repo` | `-r` | `""` | Repository name or path (defaults to current directory) |
| `--wait` | `-w` | `false` | Wait for the daemon-triggered job to finish and report its exit code/duration |
| `--local` | | `false` | Execute locally instead of sending a trigger request to the running daemon |

#### `run` vs `trigger`

| Aspect | `dcron run` | `dcron trigger` |
|---|---|---|
| Execution target | Always foreground local | Running daemon (falls back to local if stopped) |
| Output streaming | Streams to terminal + log file | Daemon writes to log file (local fallback streams to terminal + log file) |
| Updates `state.json` | No | Yes (daemon updates; local fallback updates only when daemon is stopped) |
| Overlap policy (`skip`) | Ignored | Enforced by daemon (returns conflict error if already running) |

---

## Daemon Commands

### `dcron daemon run`

Runs the scheduler daemon in the foreground until `SIGINT` or `SIGTERM` is received.

```bash
dcron daemon run
```

While running, the daemon:

1. Loads previous telemetry from `state.json` (clearing stale `running` flags) and records its PID, startup time, and OS boot ID/boot time.
2. Starts an authenticated loopback HTTP control server (`127.0.0.1:0`) and writes connection metadata (`addr`, `token`, `pid`) to a mode-`0600` `control.json` file.
3. Watches `registry.toml` and each registered repository's parent directory (250ms debounce) so atomic editor saves and `git pull` trigger live reloads.
4. Applies **validate-before-swap** reloads: if an edited `.cron.toml` fails validation, the previously scheduled jobs stay active and the error is surfaced in `dcron status` and the TUI.
5. Tracks `last_boot_id` for `on reboot` / delayed reboot jobs so config reloads and daemon restarts do not re-fire them during the same OS boot.
6. Shuts down the control server, file watcher, and scheduler cleanly on exit, removes `control.json`, and marks `pid = 0` in `state.json`.

---

### `dcron daemon install`

Installs and starts `dcron daemon run` as a user-level OS background service pointing to the current `dcron` binary:

```bash
dcron daemon install
```

| Platform | Service Backend | Service File Location |
|---|---|---|
| Linux | `systemd --user` (`systemctl --user`) | `$XDG_CONFIG_HOME/systemd/user/dcron.service` (default `~/.config/systemd/user/dcron.service`) |
| macOS | `launchd` LaunchAgent (`launchctl`) | `~/Library/LaunchAgents/com.012e.dcron.plist` |
| Windows | Windows Task Scheduler (`schtasks`) | `%LOCALAPPDATA%\dcron\dcron-task.xml` |

---

### `dcron daemon uninstall`

Stops and removes the user-level OS background service (`systemd --user`, `launchd`, or Windows Task Scheduler) and deletes its unit/plist/XML definition file.

```bash
dcron daemon uninstall
```

---

### `dcron daemon status`

Displays status output from the platform's service manager (`systemctl --user status`, `launchctl print`, or `schtasks /Query`).

```bash
dcron daemon status
```

---

## Interactive Dashboard Command

### `dcron tui` (alias: `dcron ui`)

Launches the full-screen Bubble Tea terminal dashboard.

```bash
dcron tui
dcron ui --repo backend --refresh 1s
```

| Flag | Short | Default | Description |
|---|---|---|---|
| `--repo` | `-r` | `""` | Pre-select a repository by alias or path on startup |
| `--refresh` | | `2s` | Auto-refresh interval for daemon state and active log views (must be positive) |

See the **[TUI Dashboard Guide](tui.md)** for full layout and keybinding details.

---

## On-Disk Files & Paths

| File / Directory | Linux / macOS | Windows |
|---|---|---|
| Registry | `$XDG_CONFIG_HOME/dcron/registry.toml` (default `~/.config/dcron/registry.toml`) | `%AppData%\dcron\registry.toml` (or `$XDG_CONFIG_HOME`) |
| State snapshot | `$XDG_STATE_HOME/dcron/state.json` (default `~/.local/state/dcron/state.json`) | `%LOCALAPPDATA%\dcron\state.json` (or `$XDG_STATE_HOME`) |
| Daemon control file | `<state-dir>/control.json` (mode `0600`, exists only while daemon runs) | `<state-dir>\control.json` |
| Per-job logs | `<state-dir>/logs/<repoSlug>-<hash>/<jobSlug>.log` | `<state-dir>\logs\<repoSlug>-<hash>\<jobSlug>.log` |
| Service definition | Linux: `~/.config/systemd/user/dcron.service`<br>macOS: `~/Library/LaunchAgents/com.012e.dcron.plist` | `%LOCALAPPDATA%\dcron\dcron-task.xml` |
