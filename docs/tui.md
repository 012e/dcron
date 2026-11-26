# TUI Dashboard Guide

`dcron` includes an interactive terminal dashboard built with Bubble Tea for browsing repositories, monitoring job health, inspecting schedules, streaming logs, triggering jobs on demand, editing `.cron.toml`, and managing the background daemon service.

## Launching

```bash
dcron tui
# or using the short alias:
dcron ui

# Pre-select a repository and refresh every second:
dcron tui --repo myrepo --refresh 1s
```

| Flag | Short | Default | Description |
|---|---|---|---|
| `--repo` | `-r` | `""` | Pre-select a repository by alias or path |
| `--refresh` | | `2s` | Polling interval for reloading daemon state and active logs |

The TUI runs in the alternate screen buffer with mouse wheel scrolling enabled.

---

## Layout & Panes

The interface consists of a top header bar, three interactive panes, and a contextual footer bar.

### Wide Layout (`>= 90` columns)

```text
┌─ Header: dcron | ● DAEMON RUNNING (pid, up) | Repos | Jobs | Running | Failed | Errors | updated HH:MM:SS ─┐
│ ┌─ [1] Repositories ──┐ ┌─ [2] Jobs — <scope> (N) ───────────────────────────────────────────────────────┐ │
│ │ ▸ All Repositories  │ │ REPO   JOB    STATUS     SCHEDULE    LAST RUN   NEXT RUN   RUNS                │ │
│ │   repo-a        [3] │ │ ...                                                                            │ │
│ │   repo-b       [1✗] │ └────────────────────────────────────────────────────────────────────────────────┘ │
│ │   repo-c        [!] │ ┌─ [3] Details (d) | Logs (L) ───────────────────────────────────────────────────┐ │
│ │                     │ │ Job details, upcoming run times, env vars, or live-tailed log viewport         │ │
│ └─────────────────────┘ └────────────────────────────────────────────────────────────────────────────────┘ │
└─ Footer: contextual keybinding hints (or temporary status toast notifications) ────────────────────────────┘
```

### Narrow Layout (`< 90` columns)

On narrower terminals, the three panes automatically stack vertically (`[1] Repositories` on top, `[2] Jobs` in the middle, `[3] Inspector` at the bottom), and the Jobs table hides secondary columns (`REPO`, `LAST RUN`, `RUNS`) as needed to keep job names, statuses, and schedules readable.

### Pane Breakdown

1. **`[1] Repositories`**
   - Lists `All Repositories` (index `0`) followed by each registered repository in alphabetical order.
   - Displays a compact right-aligned badge for every repository:
     - `[N]` — Number of configured jobs
     - `[!]` — Repository has a `.cron.toml` parse/validation error (live or in daemon state)
     - `[N✗]` — Number of jobs whose last run failed
     - `[N●]` — Number of currently running jobs
2. **`[2] Jobs`**
   - Shows jobs for the selected repository (or all repositories when `All Repositories` is selected), combining live `.cron.toml` configuration with runtime telemetry from `state.json`.
   - Columns adapt to terminal width: `REPO` (shown in `All` view when width allows), `JOB`, `STATUS`, `SCHEDULE`, `LAST RUN`, `NEXT RUN`, and `RUNS`.
   - Status indicators:
     - `⠋ running` — Currently executing in the daemon
     - `⠋ running*` — Currently executing as an in-TUI local manual run (cancellable with `c`)
     - `✓ ok` — Last run exited with `0`
     - `✗ exit N` / `✗ failed` — Last run failed
     - `⊘ disabled` — Job has `enabled = false`
     - `○ idle` — Enabled with no runs recorded yet
   - `NEXT RUN` shows the daemon's scheduled next run when the daemon is running, or an estimated `~in ...` preview computed from the schedule when the daemon is stopped. Reboot jobs display `ran boot`, a countdown for delayed reboot runs, or `on boot`.
3. **`[3] Inspector`**
   - **`Details (d)` tab**: Displays any repository config error banner, job identity, description, raw and normalized schedule, command, working directory, resolved settings (`shell`, `timeout`, `overlap`, `enabled`), last run telemetry, last error, next 3 upcoming run times (or reboot status), sorted environment variables, and log file path.
   - **`Logs (L)` tab**: Displays up to the last **400 lines** of the selected job's log file in a scrollable viewport. `=== [...] START ... ===` and `=== [...] END ... ===` headers are color-highlighted, and the tab header shows the `follow` indicator (`● follow: on (f)` / `follow: off (f)`) and scroll percentage.
4. **Footer & Toast Notifications**
   - Normally displays context-aware keybinding hints (standard, filter mode, or log-viewer mode).
   - Displays temporary info, success, or error toast messages (5 seconds) after actions like triggering a job, validating a config, registering/unregistering a repo, or toggling log follow.

---

## Keyboard & Mouse Shortcuts

### Global Shortcuts

| Key | Action |
|---|---|
| `Tab` / `Shift+Tab` | Cycle focus across panes (`Repositories` → `Jobs` → `Inspector`) |
| `1` / `2` / `3` | Jump directly to `[1] Repositories`, `[2] Jobs`, or `[3] Inspector` |
| `d` | Switch Inspector to the **Details** tab |
| `L` | Switch Inspector to the **Logs** tab, focus Inspector, and reload logs |
| `f` | Toggle live log auto-follow (`on` jumps to bottom and stays pinned on refresh) |
| `/` | Open live job filter (matches job name, repo name, raw/normalized schedule, command, and description) |
| `Esc` | Clear active filter query (or close an open modal) |
| `r` | Refresh daemon state, configs, and selected job logs immediately |
| `t` / `x` | Trigger the selected job (via daemon if running, otherwise locally) |
| `c` | Cancel an active in-TUI local run for the selected job |
| `a` | Open the **Register Repository** modal |
| `u` | Open the **Unregister Repository** confirmation modal |
| `e` | Open the selected repository's `.cron.toml` in your editor |
| `v` | Validate the selected repository's `.cron.toml` and show result in toast |
| `s` | Open the **Daemon & Service Manager** modal |
| `?` | Toggle the **Keyboard Shortcuts** help modal |
| `q` / `Ctrl+C` | Cancel any active in-TUI local runs and quit |

### Pane-Specific Navigation

#### `[1] Repositories`

| Key | Action |
|---|---|
| `↑` / `k`, `↓` / `j` | Move repository selection |
| `g` / `Home`, `G` / `End` | Jump to `All Repositories` / last repository |
| `→` / `l` / `Enter` | Move focus to `[2] Jobs` |

#### `[2] Jobs`

| Key | Action |
|---|---|
| `↑` / `k`, `↓` / `j` | Move job selection |
| `PgUp` / `Ctrl+U`, `PgDn` / `Ctrl+D` | Jump 5 jobs up / down |
| `g` / `Home`, `G` / `End` | Jump to first / last job |
| `←` / `h` | Move focus to `[1] Repositories` |
| `→` / `l` | Move focus to `[3] Inspector` |
| `Enter` | Trigger the selected job |

#### `[3] Inspector`

| Key | Action |
|---|---|
| `←` / `h` | From **Logs**, switch to **Details**; from **Details**, move focus back to `[2] Jobs` |
| `→` / `l` | From **Details**, switch to **Logs** and load logs |
| `↑` / `k`, `↓` / `j` | *(Logs tab)* Scroll up / down 1 line |
| `PgUp` / `Ctrl+U`, `PgDn` / `Ctrl+D` | *(Logs tab)* Scroll half-page up / down |
| `g` / `Home` | *(Logs tab)* Jump to top (pauses auto-follow) |
| `G` / `End` | *(Logs tab)* Jump to bottom (re-enables auto-follow) |

#### Filter Input Mode (`/`)

| Key | Action |
|---|---|
| *Typing* | Filters the Jobs table live as you type |
| `Enter` | Apply filter and return focus to the Jobs table |
| `Esc` | Clear filter and close the filter input |

#### Mouse Support

- **Scroll Wheel Up / Down**: Scrolls the currently focused pane (`Repositories`, `Jobs`, or `Logs` viewport). Scrolling up in Logs automatically pauses auto-follow; scrolling back to the bottom re-enables it.

---

## Modals & Workflows

### Register Repository (`a`)

Pre-fills the path input with the current working directory and supports `~/` expansion.

- `Tab` / `Shift+Tab` / `↑` / `↓` — Switch between **Repository Path** and **Alias Name (optional)**
- `Enter` — Validate `<path>/.cron.toml` and register the repository
- `Ctrl+N` — Create a starter `.cron.toml` (if one does not exist) and register the repository
- `Esc` — Cancel

### Unregister Repository (`u`)

Targets the currently selected repository (or the repository owning the currently selected job when viewing `All Repositories`). Removing a repository only updates `registry.toml` and never deletes `.cron.toml`.

- `y` / `Y` / `Enter` — Confirm unregister
- `n` / `N` / `Esc` / `q` — Cancel

### Editing `.cron.toml` (`e`)

Temporarily suspends the TUI and opens the selected repository's `.cron.toml` using:

1. `$VISUAL`
2. `$EDITOR`
3. Fallback: `vi` on Unix/macOS, `notepad` on Windows

When you save and exit the editor, the TUI automatically resumes and reloads the repository snapshot (and the running daemon automatically picks up the saved file via its directory watcher).

### Triggering & Cancelling Jobs (`t` / `x` / `Enter`, `c`)

- **When the daemon is running**: The TUI sends an authenticated trigger request to the daemon's loopback control server (`control.json`), keeping state, logs, and overlap policies unified.
- **When the daemon is stopped** (or unreachable): The TUI automatically falls back to running the job locally in the background, marks it `running*`, writes output to the job's log file, overlays the result in the UI, and persists telemetry to `state.json` (only while no daemon process is alive).
- Press `c` to cancel an in-progress local (`running*`) run. Exiting the TUI (`q` or `Ctrl+C`) also cancels any in-TUI local runs.

### Daemon & Service Manager (`s`)

Displays the live daemon process state (PID, uptime, OS boot ID), OS service definition path, and the first 10 lines of the platform service manager's status output (`systemd --user` on Linux, `launchd` on macOS, Task Scheduler on Windows).

- `i` — Install and start the OS background service
- `U` — Stop and uninstall the OS background service
- `r` — Refresh service status
- `Esc` / `q` / `s` — Close modal
