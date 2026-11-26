# Cron & Schedule Syntax Reference

`dcron` supports a **hybrid schedule syntax** in `.cron.toml` that lets you mix standard 5-field cron expressions, cron `@descriptors`, human-friendly natural language, fixed intervals, and boot-aware reboot triggers.

Expressions are **case-insensitive** and ignore extra surrounding or internal whitespace. All cron schedules run in the machine's **local timezone**.

---

## Schedule Kinds

Every schedule parses into one of three internal kinds:

| Kind | Behavior | Examples |
|---|---|---|
| `cron` | Wall-clock calendar schedule (5-field cron) | `*/15 * * * *`, `every hour`, `daily at 9am` |
| `interval` | Fixed elapsed duration repeating from daemon start | `every 30s`, `every 1h30m`, `every 7 minutes`, `@every 5m` |
| `reboot` | Event-driven one-shot trigger per OS boot (immediate or delayed) | `on reboot`, `5 minutes after reboot`, `@reboot + 5m` |

Preview how `dcron` parses any schedule and inspect upcoming run times with:

```bash
dcron validate
```

---

## 1. Standard 5-Field Cron

Standard Unix 5-field cron expressions are supported (no seconds field):

```text
┌───────────── minute (0 - 59)
│ ┌─────────── hour (0 - 23)
│ │ ┌───────── day of month (1 - 31)
│ │ │ ┌─────── month (1 - 12 or JAN - DEC)
│ │ │ │ ┌───── day of week (0 - 6 or SUN - SAT; 0 = Sunday)
│ │ │ │ │
* * * * *
```

### Supported Field Operators

| Operator | Meaning | Example |
|---|---|---|
| `*` | Every value | `* * * * *` (every minute) |
| `,` | List of values | `5,35 * * * *` (at :05 and :35) |
| `-` | Inclusive range | `0 9 * * 1-5` or `0 9 * * MON-FRI` |
| `/` | Step increment | `*/15 * * * *` (every 15 minutes), `0 8-18/2 * * *` |

### Examples

| Expression | Meaning |
|---|---|
| `*/15 * * * *` | Every 15 minutes |
| `0 9 * * 1-5` | 09:00 Monday through Friday |
| `5,35 8-18 * * MON-FRI` | :05 and :35 past every hour from 08:00 to 18:00, Mon–Fri |
| `0 0 1 * *` | Midnight on the 1st of every month |

---

## 2. Cron `@descriptors`

| Descriptor | Equivalent | Kind |
|---|---|---|
| `@yearly`, `@annually` | `0 0 1 1 *` | `cron` |
| `@monthly` | `0 0 1 * *` | `cron` |
| `@weekly` | `0 0 * * 0` | `cron` |
| `@daily`, `@midnight` | `0 0 * * *` | `cron` |
| `@hourly` | `0 * * * *` | `cron` |
| `@every <duration>` | Positive Go duration (e.g. `@every 5m`, `@every 1h30m`, `@every 30s`) | `interval` |
| `@reboot` | Once per OS boot | `reboot` |

---

## 3. Natural-Language Schedules

### Single-Word Shorthands

| Input | Parsed As |
|---|---|
| `hourly` | `0 * * * *` |
| `daily` | `0 0 * * *` |
| `weekly` | `0 0 * * 0` |
| `monthly` | `0 0 1 * *` |
| `yearly`, `annually` | `0 0 1 1 *` |

### Intervals (`every ...`)

Supports singular units, `<N> <unit>` counts, and compound Go durations:

| Input | Parsed As | Kind |
|---|---|---|
| `every second` / `every sec` | `1s` | `interval` |
| `every 30s` / `every 45 seconds` | `30s` / `45s` | `interval` |
| `every minute` / `every min` | `* * * * *` | `cron` |
| `every 5 minutes` / `every 15 mins` | `*/5 * * * *` / `*/15 * * * *` | `cron` |
| `every 7 minutes` *(non-divisor of 60)* | `7m` | `interval` |
| `every hour` / `every hr` | `0 * * * *` | `cron` |
| `every 2 hours` / `every 6 hrs` | `0 */2 * * *` / `0 */6 * * *` | `cron` |
| `every 5 hours` *(non-divisor of 24)* | `5h` | `interval` |
| `every 1h30m` / `every 2m30s` | `1h30m` / `2m30s` | `interval` |
| `every day` / `every 1 day` | `0 0 * * *` | `cron` |
| `every 3 days` | `72h` | `interval` |
| `every week` / `every 1 week` | `0 0 * * 0` | `cron` |
| `every 2 weeks` | `336h` | `interval` |
| `every month` | `0 0 1 * *` | `cron` |
| `every year` | `0 0 1 1 *` | `cron` |

> **Cron vs. Interval alignment:** `every N minutes` compiles to wall-clock cron (`*/N * * * *`) when `N` evenly divides `60` (`1, 2, 3, 4, 5, 6, 10, 12, 15, 20, 30`). Similarly, `every N hours` compiles to `0 */N * * *` when `N` evenly divides `24` (`1, 2, 3, 4, 6, 8, 12`). Other values (e.g., `every 7 minutes`, `every 5 hours`, `every 3 days`) run as fixed intervals.

### Hourly at a Specific Minute

Syntax: `(every hour | hourly) at (:<MM> | minute <MM>)` where `MM` is `0–59`.

| Input | Parsed As |
|---|---|
| `every hour at :15` | `15 * * * *` |
| `hourly at :30` | `30 * * * *` |
| `every hour at minute 5` | `5 * * * *` |

### Daily & Time-Only Shorthands

| Input | Parsed As |
|---|---|
| `at 9am` | `0 9 * * *` |
| `at 14:30` | `30 14 * * *` |
| `at noon` | `0 12 * * *` |
| `at midnight` | `0 0 * * *` |
| `daily at 9am` | `0 9 * * *` |
| `every day at 14:30` | `30 14 * * *` |
| `every day at midnight` | `0 0 * * *` |

### Weekdays, Weekends & Specific Days of the Week

Syntax: `[weekly on | every week on | every] <days> [at <time>]`

When `at <time>` is omitted, the time defaults to **midnight (`00:00`)**.

- **Groups**: `every weekday` / `weekdays` (`1-5`), `every weekend` / `weekends` (`0,6`)
- **Day names**: `sun`/`sunday`/`sundays`, `mon`/`monday`/`mondays`, `tue`/`tues`/`tuesday`/`tuesdays`, `wed`/`weds`/`wednesday`/`wednesdays`, `thu`/`thur`/`thurs`/`thursday`/`thursdays`, `fri`/`friday`/`fridays`, `sat`/`saturday`/`saturdays`
- **Lists**: Separate multiple days with `,` and/or `and` (e.g. `mon,wed,fri`, `tue and thu`, `mon, wed and fri`).

| Input | Parsed As |
|---|---|
| `every weekday at 8:30am` | `30 8 * * 1-5` |
| `weekdays at 09:00` | `0 9 * * 1-5` |
| `every weekend at 10am` | `0 10 * * 0,6` |
| `every monday at 9am` | `0 9 * * 1` |
| `mondays at 9:15pm` | `15 21 * * 1` |
| `every mon,wed,fri at 8am` | `0 8 * * 1,3,5` |
| `every tue and thu at 18:00` | `0 18 * * 2,4` |
| `weekly on sun at 2am` | `0 2 * * 0` |
| `every friday` | `0 0 * * 5` |

### Monthly Schedules

Syntax: `(monthly | every month) on [the | day] <1-31>[st|nd|rd|th] [at <time>]`

When `at <time>` is omitted, the time defaults to **midnight (`00:00`)**.

| Input | Parsed As |
|---|---|
| `monthly on the 1st at 00:00` | `0 0 1 * *` |
| `every month on day 15 at 9am` | `0 9 15 * *` |
| `monthly on 2nd at 6:30pm` | `30 18 2 * *` |
| `every month on the 10th` | `0 0 10 * *` |

### Supported Time-of-Day Formats (`<time>`)

| Format | Examples | Notes |
|---|---|---|
| Named | `midnight` (`00:00`), `noon` (`12:00`) | |
| 12-hour | `9am`, `8:30am`, `12am` (`00:00`), `12pm` (`12:00`), `6:30pm` (`18:30`) | Hour `1–12`, optional `:MM` (`00–59`), optional space before `am`/`pm` |
| 24-hour | `09:00`, `14:30`, `0:05`, `23:59` | Hour `0–23`, minute `00–59` |

---

## 4. Reboot & Startup Triggers

Reboot schedules run **once per OS boot**. `dcron` persists the OS boot identifier (`last_boot_id` in `state.json`) so config reloads and daemon restarts do not re-trigger reboot jobs during the same boot session.

### Immediate Reboot Aliases

All of the following parse as `@reboot` (`KindReboot`, delay `0`):

- `@reboot`, `reboot`
- `on reboot`, `at reboot`, `after reboot`
- `on boot`, `at boot`, `after boot`
- `on startup`, `at startup`, `after startup`, `startup`

### Delayed Reboot Triggers

Run once after a positive delay from OS boot time (normalized as `@reboot +<duration>`):

1. **Natural language**: `<duration> after [the] [system] (@reboot | reboot | boot | startup)`
2. **Plus syntax**: `(@reboot | reboot | on reboot | at reboot | on boot | at boot | on startup | at startup | startup) + <duration>`

`<duration>` accepts standard Go durations (`500ms`, `30s`, `5m`, `1h30m`) or natural-language single/compound durations with optional `,` and `and` separators:

- **Articles**: `a`, `an` (interpreted as `1`, e.g. `a minute after reboot`, `an hour after startup`)
- **Units**:
  - Milliseconds: `ms`, `msec`, `msecs`, `millisecond`, `milliseconds`
  - Seconds: `s`, `sec`, `secs`, `second`, `seconds`
  - Minutes: `m`, `min`, `mins`, `minute`, `minutes`
  - Hours: `h`, `hr`, `hrs`, `hour`, `hours`
  - Days: `d`, `day`, `days` (`24h`)
  - Weeks: `w`, `wk`, `wks`, `week`, `weeks` (`7d`)

| Input | Normalized Summary |
|---|---|
| `5 minutes after reboot` | `@reboot +5m` |
| `30s after startup` | `@reboot +30s` |
| `a minute after reboot` | `@reboot +1m` |
| `an hour after boot` | `@reboot +1h` |
| `1 hour and 30 minutes after system boot` | `@reboot +1h30m` |
| `2 minutes, 30 seconds after the reboot` | `@reboot +2m30s` |
| `1 day after reboot` | `@reboot +24h` |
| `@reboot + 5m` | `@reboot +5m` |
| `on reboot + 5 minutes` | `@reboot +5m` |
| `at startup + 30s` | `@reboot +30s` |

### How Reboot Delays Are Scheduled

- When the OS boot timestamp is available (Linux `/proc/uptime`, macOS `kern.boottime`, Windows `DurationSinceBoot`), `dcron` schedules the job at `boot_time + delay`.
- If OS boot time is unavailable on the platform, it falls back to daemon startup time + delay.
- If `boot_time + delay` is already in the past (and the job has not run during this OS boot), the daemon runs the job immediately.

---

## 5. `.cron.toml` Schema Reference

Jobs are defined in `<repo>/.cron.toml`. Unknown keys are **rejected** during validation so typos are caught immediately.

```toml
[defaults]
shell = "/bin/bash"       # optional (defaults to $SHELL, /bin/sh on Unix, $COMSPEC/cmd.exe on Windows)
timeout = "10m"           # optional positive Go duration (default: no timeout)
overlap = "skip"          # optional: "skip" (default) | "wait" | "allow"
dir = "."                 # optional working directory relative to repo root (or absolute)
env = { ENV = "local" }   # optional map of default environment variables

[jobs.sync]
description = "Sync local data"   # optional human-readable description
schedule = "every hour"           # required (any syntax documented above)
command = "./scripts/sync.sh"     # required shell command
enabled = true                    # optional (default: true)
timeout = "5m"                    # optional override of [defaults].timeout
overlap = "wait"                  # optional override of [defaults].overlap
shell = "/bin/sh"                 # optional override of [defaults].shell
dir = "tools"                     # optional override of [defaults].dir
env = { VERBOSE = "1" }           # merged on top of [defaults].env
```

### Overlap Policies

| Policy | Behavior when a job is triggered while already running |
|---|---|
| `skip` *(default)* | Skip the overlapping run and wait for the next scheduled trigger (manual daemon triggers return a conflict error) |
| `wait` | Queue the next run until the active run completes |
| `allow` | Allow concurrent executions of the same job |
