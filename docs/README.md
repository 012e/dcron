# dcron Documentation

Welcome to the `dcron` documentation.

## Guides

- **[CLI Reference](cli.md)** — Complete reference for every `dcron` subcommand, flag, target shorthand, daemon control workflow, and on-disk config/state/log paths.
- **[TUI Dashboard Guide](tui.md)** — Interactive terminal UI layout, status indicators, keyboard/mouse shortcuts, modals, log viewer, and job-triggering behavior.
- **[Cron & Schedule Syntax](cron-syntax.md)** — Complete reference for 5-field cron, `@descriptors`, natural-language schedules, immediate/delayed reboot triggers, and `.cron.toml` schema.

## Quick Overview

`dcron` lets you keep scheduled jobs next to your code in a repo-local `.cron.toml` file, run them automatically via a lightweight background daemon, and inspect or trigger them from either the CLI or the interactive TUI (`dcron tui` / `dcron ui`).

See the project [README](../README.md) for installation instructions, an example `.cron.toml`, and the supported hybrid schedule syntax table.
