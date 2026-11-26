# dcron

A CLI tool and lightweight background daemon for managing repo-local cron jobs via `.cron.toml`.

## Features

- **Repo-local configuration**: Define scheduled jobs right next to your code in `.cron.toml`.
- **Hybrid schedule syntax**: Mix natural language (`every hour`, `every 15 minutes`, `daily at 9am`, `every weekday at 8:30am`, `on reboot`) with standard 5-field cron (`*/15 * * * *`) and `@descriptors` (`@hourly`, `@daily`, `@reboot`, `@every 5m`).
- **Live auto-reload**: Powered by `gocron/v2` and `fsnotify` (watching parent directories to handle Vim/VS Code atomic saves and `git pull`), with validate-before-swap so typos mid-edit never break running schedules.
- **Boot-aware `on reboot` jobs**: Tracks Linux kernel `boot_id` so reboot jobs run once per actual OS boot rather than on every daemon restart.
- **Per-job logs, timeouts & overlap control**: Structured per-job log files, process-group timeout cleanup, and `skip | wait | allow` overlap policies.

## Quick Start

```bash
# Build / install
go install ./cmd/dcron

# Initialize a starter .cron.toml in your repo
dcron init

# Validate syntax and preview next run times
dcron validate

# Register the repo with dcron
dcron register

# Test a job immediately in the foreground
dcron run sync

# Run the daemon in the foreground, or install as a systemd --user service
dcron daemon run
# or:
dcron daemon install

# Check status and logs
dcron status
dcron logs sync
```

## Example `.cron.toml`

```toml
[defaults]
shell = "/bin/bash"
timeout = "10m"
overlap = "skip" # skip | wait | allow
env = { ENV = "local" }

[jobs.sync]
description = "Sync assets every hour"
schedule = "every hour"
command = "./scripts/sync.sh"

[jobs.boot_warmup]
description = "Run once when the machine boots"
schedule = "on reboot"
command = "make warmup"

[jobs.weekday_report]
schedule = "every weekday at 9am" # compiles to 0 9 * * 1-5
command = "./scripts/report.sh"
dir = "tools"

[jobs.custom_cron]
schedule = "*/15 * * * *"
command = "./scripts/check.sh"
```

## Supported Schedule Syntax

| Input | Parsed As |
|---|---|
| `on reboot`, `at startup`, `@reboot` | `@reboot` (runs once per OS boot) |
| `every 30s`, `every 1h30m`, `@every 5m` | Interval duration |
| `every minute`, `every 5 minutes`, `every hour`, `every 2 hours` | Cron (`* * * * *`, `*/5 * * * *`, `0 * * * *`, `0 */2 * * *`) |
| `every hour at :15` | `15 * * * *` |
| `daily at 9am`, `every day at 14:30`, `at noon` | `0 9 * * *`, `30 14 * * *`, `0 12 * * *` |
| `every weekday at 8:30am` | `30 8 * * 1-5` |
| `every weekend at 10am` | `0 10 * * 0,6` |
| `every mon,wed,fri at 8am` | `0 8 * * 1,3,5` |
| `monthly on the 1st at 00:00` | `0 0 1 * *` |
| `0 9 * * 1-5`, `@hourly`, `@daily`, `@weekly` | Standard 5-field cron |
