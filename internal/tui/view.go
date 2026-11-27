package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/012e/dcron/internal/schedule"
)

type layoutMetrics struct {
	splitMode       bool // true = sidebar + right stack; false = narrow vertical stack
	headerH         int
	footerH         int
	bodyH           int
	sidebarOuterW   int
	sidebarInnerW   int
	sidebarInnerH   int
	rightOuterW     int
	jobsOuterH      int
	jobsInnerW      int
	jobsInnerH      int
	inspectorOuterH int
	inspectorInnerW int
	inspectorInnerH int
}

func (m Model) computeLayout() layoutMetrics {
	w := m.width
	h := m.height
	if w <= 0 {
		w = 100
	}
	if h <= 0 {
		h = 30
	}

	headerH := 1
	footerH := 1
	bodyH := max(10, h-headerH-footerH)

	splitMode := w >= 90
	var lm layoutMetrics
	lm.splitMode = splitMode
	lm.headerH = headerH
	lm.footerH = footerH
	lm.bodyH = bodyH

	if splitMode {
		sidebarOuterW := min(32, max(24, w/4))
		rightOuterW := max(40, w-sidebarOuterW)

		jobsOuterH := max(7, (bodyH*55)/100)
		inspectorOuterH := max(6, bodyH-jobsOuterH)
		if jobsOuterH+inspectorOuterH > bodyH {
			jobsOuterH = max(5, bodyH-inspectorOuterH)
		}

		lm.sidebarOuterW = sidebarOuterW
		lm.sidebarInnerW = max(10, sidebarOuterW-2)
		lm.sidebarInnerH = max(4, bodyH-2)
		lm.rightOuterW = rightOuterW
		lm.jobsOuterH = jobsOuterH
		lm.jobsInnerW = max(20, rightOuterW-2)
		lm.jobsInnerH = max(3, jobsOuterH-2)
		lm.inspectorOuterH = inspectorOuterH
		lm.inspectorInnerW = max(20, rightOuterW-2)
		lm.inspectorInnerH = max(3, inspectorOuterH-2)
	} else {
		// Narrow mode: compact repos strip on top, jobs in middle, inspector at bottom.
		repoOuterH := min(6, max(4, bodyH/5))
		remH := max(8, bodyH-repoOuterH)
		jobsOuterH := max(5, remH/2)
		inspectorOuterH := max(5, remH-jobsOuterH)

		lm.sidebarOuterW = w
		lm.sidebarInnerW = max(10, w-2)
		lm.sidebarInnerH = max(2, repoOuterH-2)
		lm.rightOuterW = w
		lm.jobsOuterH = jobsOuterH
		lm.jobsInnerW = max(20, w-2)
		lm.jobsInnerH = max(3, jobsOuterH-2)
		lm.inspectorOuterH = inspectorOuterH
		lm.inspectorInnerW = max(20, w-2)
		lm.inspectorInnerH = max(3, inspectorOuterH-2)
	}

	return lm
}

func (m Model) jobsVisibleRows(lm layoutMetrics) int {
	// Inside jobs pane: title row (1) + optional filter row (0 or 1) + table header (2: text + border)
	overhead := 3
	if m.filtering || m.filterQuery != "" {
		overhead++
	}
	return max(1, lm.jobsInnerH-overhead)
}

// View renders the entire TUI screen.
func (m Model) View() string {
	if !m.ready {
		return "Loading dcron dashboard..."
	}

	lm := m.computeLayout()

	header := m.renderHeader()
	footer := m.renderFooter()

	var body string
	if lm.splitMode {
		sidebar := m.renderReposPane(lm)
		right := lipgloss.JoinVertical(
			lipgloss.Left,
			m.renderJobsPane(lm),
			m.renderInspectorPane(lm),
		)
		body = lipgloss.JoinHorizontal(lipgloss.Top, sidebar, right)
	} else {
		body = lipgloss.JoinVertical(
			lipgloss.Left,
			m.renderReposPane(lm),
			m.renderJobsPane(lm),
			m.renderInspectorPane(lm),
		)
	}

	base := lipgloss.JoinVertical(lipgloss.Left, header, body, footer)

	if m.modal != modalNone {
		return m.renderModalOverlay()
	}

	return base
}

func (m Model) renderHeader() string {
	title := styleTitle.Render("dcron")

	var daemonBadge string
	if m.snap.DaemonAlive {
		uptime := formatCompactDuration(max(0, m.now().Sub(m.snap.DaemonStartedAt)))
		daemonBadge = styleBadgeRunning.Render(fmt.Sprintf("● DAEMON RUNNING (pid %d, up %s)", m.snap.DaemonPID, uptime))
	} else {
		daemonBadge = styleBadgeStopped.Render("○ DAEMON STOPPED")
	}

	runningCount := m.snap.TotalRunning + len(m.manualRuns)
	metrics := []string{
		styleMetricLabel.Render("Repos:") + " " + styleMetricValue.Render(fmt.Sprintf("%d", len(m.snap.Repos))),
		styleMetricLabel.Render("Jobs:") + " " + styleMetricValue.Render(fmt.Sprintf("%d", len(m.snap.AllJobs))),
	}
	if runningCount > 0 {
		metrics = append(metrics, styleStatusRunning.Render(fmt.Sprintf("%s Running: %d", m.spinner.View(), runningCount)))
	}
	if m.snap.TotalFailed > 0 {
		metrics = append(metrics, styleMetricError.Render(fmt.Sprintf("Failed: %d", m.snap.TotalFailed)))
	}
	if m.snap.TotalErrors > 0 {
		metrics = append(metrics, styleMetricError.Render(fmt.Sprintf("Errors: %d", m.snap.TotalErrors)))
	}

	left := strings.Join([]string{title, daemonBadge, strings.Join(metrics, "  ")}, "  ")

	rightText := ""
	if !m.snap.LoadedAt.IsZero() {
		rightText = styleMuted.Render("updated " + m.snap.LoadedAt.Local().Format("15:04:05"))
	}
	if m.loadErr != "" {
		rightText = styleMetricError.Render("load error")
	}

	avail := max(0, m.width-2) // padding 0,1
	gap := max(1, avail-lipgloss.Width(left)-lipgloss.Width(rightText))
	line := left + strings.Repeat(" ", gap) + rightText
	if lipgloss.Width(line) > avail && avail > 4 {
		line = truncateStr(left, avail)
	}

	return styleHeaderBar.Width(m.width).Render(line)
}

func (m Model) renderReposPane(lm layoutMetrics) string {
	focused := m.focus == focusRepos
	titleStyle := stylePaneTitle
	paneStyle := stylePane
	if focused {
		titleStyle = stylePaneTitleFocused
		paneStyle = stylePaneFocused
	}

	contentW := max(8, lm.sidebarInnerW-2) // account for horizontal padding(0,1)
	header := titleStyle.Render(fmt.Sprintf("[1] Repositories (%d)", len(m.snap.Repos)))

	lines := []string{header}
	maxItems := max(1, lm.sidebarInnerH-1)

	type row struct {
		label  string
		badge  string
		hasErr bool
	}

	rows := make([]row, 0, len(m.snap.Repos)+1)
	rows = append(rows, row{
		label: "All Repositories",
		badge: fmt.Sprintf("%d", len(m.snap.AllJobs)),
	})
	for _, r := range m.snap.Repos {
		badge := fmt.Sprintf("%d", len(r.Jobs))
		if r.HasError() {
			badge = "!"
		} else if r.FailedCount > 0 {
			badge = fmt.Sprintf("%d✗", r.FailedCount)
		} else if r.RunningCount > 0 {
			badge = fmt.Sprintf("%d●", r.RunningCount)
		}
		rows = append(rows, row{
			label:  r.Entry.Name,
			badge:  badge,
			hasErr: r.HasError(),
		})
	}

	// Scroll window for repos list if there are more repos than fit.
	start := 0
	if m.repoIdx >= maxItems {
		start = m.repoIdx - maxItems + 1
	}
	end := min(len(rows), start+maxItems)

	for i := start; i < end; i++ {
		r := rows[i]
		isSelected := i == m.repoIdx
		prefix := "  "
		if isSelected {
			prefix = "▸ "
		}

		badgePlain := "[" + r.badge + "]"
		maxLabelW := max(4, contentW-len(prefix)-len(badgePlain)-1)
		name := truncateStr(r.label, maxLabelW)
		pad := max(1, contentW-len(prefix)-lipgloss.Width(name)-len(badgePlain))

		if isSelected {
			rowText := prefix + name + strings.Repeat(" ", pad) + badgePlain
			if focused {
				lines = append(lines, styleRowSelected.Width(contentW).Render(rowText))
			} else {
				lines = append(lines, styleRowSelectedDim.Width(contentW).Render(rowText))
			}
		} else {
			styledBadge := styleMuted.Render(badgePlain)
			if r.hasErr {
				styledBadge = styleStatusFailed.Render(badgePlain)
			}
			line := prefix + name + strings.Repeat(" ", pad) + styledBadge
			lines = append(lines, line)
		}
	}

	if len(m.snap.Repos) == 0 && len(lines) < lm.sidebarInnerH {
		lines = append(lines, "", styleMuted.Render("Press 'a' to register"))
	}

	return paneStyle.
		Width(lm.sidebarInnerW).
		Height(lm.sidebarInnerH).
		Render(strings.Join(lines, "\n"))
}

func (m Model) renderJobsPane(lm layoutMetrics) string {
	focused := m.focus == focusJobs
	titleStyle := stylePaneTitle
	paneStyle := stylePane
	if focused {
		titleStyle = stylePaneTitleFocused
		paneStyle = stylePaneFocused
	}

	contentW := max(20, lm.jobsInnerW-2)
	jobs := m.filteredJobs()

	scope := "All"
	if repo, ok := m.selectedRepo(); ok {
		scope = repo.Entry.Name
	}
	title := titleStyle.Render(fmt.Sprintf("[2] Jobs — %s (%d)", scope, len(jobs)))
	if m.filterQuery != "" && !m.filtering {
		title += "  " + styleCyan.Render(fmt.Sprintf("[filter: %s]", m.filterQuery))
	}

	lines := []string{title}

	if m.filtering {
		lines = append(lines, m.filterInput.View())
	} else if m.filterQuery != "" {
		lines = append(lines, styleMuted.Render(fmt.Sprintf("Filter active: %q (press / to edit, esc to clear)", m.filterQuery)))
	}

	showRepoCol := m.repoIdx == 0 && contentW >= 68
	cols := computeJobColumns(contentW, showRepoCol)

	headerRow := renderJobRowText(cols, showRepoCol, "REPO", "JOB", "STATUS", "SCHEDULE", "LAST RUN", "NEXT RUN", "RUNS")
	lines = append(lines, styleTableHeader.Width(contentW).Render(headerRow))

	if len(jobs) == 0 {
		emptyMsg := "No jobs found."
		if len(m.snap.Repos) == 0 {
			emptyMsg = "No repositories or global crons configured. Press 'a' to add a repo or run `dcron init --global`."
		} else if repo, ok := m.selectedRepo(); ok && repo.HasError() {
			cfgName := ".cron.toml"
			if repo.IsGlobal {
				cfgName = "global.toml"
			}
			emptyMsg = fmt.Sprintf("Configuration %s has an error (see Details below, press 'e' to edit).", cfgName)
		} else if m.filterQuery != "" {
			emptyMsg = fmt.Sprintf("No jobs match filter %q. Press esc to clear.", m.filterQuery)
		}
		lines = append(lines, styleMuted.Render("  "+emptyMsg))
	} else {
		visibleRows := m.jobsVisibleRows(lm)
		start := m.jobScrollOffset
		if start < 0 {
			start = 0
		}
		if start > len(jobs) {
			start = max(0, len(jobs)-1)
		}
		end := min(len(jobs), start+visibleRows)
		now := m.now()

		for i := start; i < end; i++ {
			j := m.enrichJobWithManualResult(jobs[i])
			isSelected := i == m.jobIdx
			manualRun := m.isManualRunning(j.Key())
			status := j.EffectiveStatus(manualRun)

			statusPlain := formatStatusPlain(status, manualRun, m.spinner.View(), j.LastExitCode)
			lastRunStr := formatLastRunShort(j, now)
			nextRunStr := formatNextRunShort(j, m.snap.DaemonAlive, m.snap.BootID, now)
			runsStr := fmt.Sprintf("%d", j.RunCount)

			if isSelected {
				plain := renderJobRowText(cols, showRepoCol, j.RepoName, j.Name, statusPlain, j.ScheduleSummary, lastRunStr, nextRunStr, runsStr)
				if focused {
					lines = append(lines, styleRowSelected.Width(contentW).Render(plain))
				} else {
					lines = append(lines, styleRowSelectedDim.Width(contentW).Render(plain))
				}
			} else {
				styledStatus := renderStyledStatus(status, padRight(truncateStr(statusPlain, cols.status), cols.status))
				row := renderStyledJobRow(cols, showRepoCol, j.RepoName, j.Name, styledStatus, j.ScheduleSummary, lastRunStr, nextRunStr, runsStr)
				lines = append(lines, row)
			}
		}
	}

	return paneStyle.
		Width(lm.jobsInnerW).
		Height(lm.jobsInnerH).
		Render(strings.Join(lines, "\n"))
}

type jobColumns struct {
	repo     int
	name     int
	status   int
	schedule int
	lastRun  int
	nextRun  int
	runs     int
}

func computeJobColumns(totalW int, showRepo bool) jobColumns {
	// Leave 1 space between columns.
	cols := jobColumns{
		status:  11,
		runs:    5,
		lastRun: 11,
		nextRun: 12,
	}

	if totalW < 64 {
		cols.lastRun = 0
		cols.runs = 0
		cols.nextRun = 10
		cols.status = 10
	} else if totalW < 80 {
		cols.runs = 4
		cols.lastRun = 10
		cols.nextRun = 11
	}

	fixed := cols.status
	gaps := 1
	if cols.lastRun > 0 {
		fixed += cols.lastRun
		gaps++
	}
	if cols.nextRun > 0 {
		fixed += cols.nextRun
		gaps++
	}
	if cols.runs > 0 {
		fixed += cols.runs
		gaps++
	}
	if showRepo {
		cols.repo = min(16, max(10, totalW/6))
		fixed += cols.repo
		gaps++
	}

	rem := max(16, totalW-fixed-gaps)
	cols.name = max(10, (rem*55)/100)
	cols.schedule = max(6, rem-cols.name)
	return cols
}

func renderJobRowText(c jobColumns, showRepo bool, repo, name, status, sched, lastRun, nextRun, runs string) string {
	parts := make([]string, 0, 7)
	if showRepo {
		parts = append(parts, padRight(truncateStr(repo, c.repo), c.repo))
	}
	parts = append(parts, padRight(truncateStr(name, c.name), c.name))
	parts = append(parts, padRight(truncateStr(status, c.status), c.status))
	parts = append(parts, padRight(truncateStr(sched, c.schedule), c.schedule))
	if c.lastRun > 0 {
		parts = append(parts, padRight(truncateStr(lastRun, c.lastRun), c.lastRun))
	}
	if c.nextRun > 0 {
		parts = append(parts, padRight(truncateStr(nextRun, c.nextRun), c.nextRun))
	}
	if c.runs > 0 {
		parts = append(parts, padLeft(truncateStr(runs, c.runs), c.runs))
	}
	return strings.Join(parts, " ")
}

func renderStyledJobRow(c jobColumns, showRepo bool, repo, name, styledStatus, sched, lastRun, nextRun, runs string) string {
	parts := make([]string, 0, 7)
	if showRepo {
		parts = append(parts, styleMuted.Render(padRight(truncateStr(repo, c.repo), c.repo)))
	}
	parts = append(parts, padRight(truncateStr(name, c.name), c.name))
	parts = append(parts, styledStatus)
	parts = append(parts, styleCyan.Render(padRight(truncateStr(sched, c.schedule), c.schedule)))
	if c.lastRun > 0 {
		parts = append(parts, styleMuted.Render(padRight(truncateStr(lastRun, c.lastRun), c.lastRun)))
	}
	if c.nextRun > 0 {
		parts = append(parts, padRight(truncateStr(nextRun, c.nextRun), c.nextRun))
	}
	if c.runs > 0 {
		parts = append(parts, styleMuted.Render(padLeft(truncateStr(runs, c.runs), c.runs)))
	}
	return strings.Join(parts, " ")
}

func formatStatusPlain(st JobStatus, manualRunning bool, spinFrame string, exitCode *int) string {
	switch st {
	case StatusRunning:
		if manualRunning {
			return spinFrame + " running*"
		}
		return spinFrame + " running"
	case StatusOK:
		return "✓ ok"
	case StatusFailed:
		if exitCode != nil {
			return fmt.Sprintf("✗ exit %d", *exitCode)
		}
		return "✗ failed"
	case StatusDisabled:
		return "⊘ disabled"
	default:
		return "○ idle"
	}
}

func renderStyledStatus(st JobStatus, text string) string {
	switch st {
	case StatusRunning:
		return styleStatusRunning.Render(text)
	case StatusOK:
		return styleStatusOK.Render(text)
	case StatusFailed:
		return styleStatusFailed.Render(text)
	case StatusDisabled:
		return styleStatusDisabled.Render(text)
	default:
		return styleStatusIdle.Render(text)
	}
}

func formatLastRunShort(j JobItem, now time.Time) string {
	if j.LastRun == nil {
		return "-"
	}
	return FormatRelativeTime(*j.LastRun, now)
}

func formatNextRunShort(j JobItem, daemonAlive bool, bootID string, now time.Time) string {
	if !j.Enabled {
		return "disabled"
	}
	if j.ScheduleKind == "reboot" {
		if j.LastBootID != "" && bootID != "" && j.LastBootID == bootID {
			return "ran boot"
		}
		if daemonAlive && j.NextRun != nil {
			return FormatRelativeTime(*j.NextRun, now)
		}
		return "on boot"
	}
	if daemonAlive && j.NextRun != nil {
		return FormatRelativeTime(*j.NextRun, now)
	}
	if j.Schedule != nil {
		if nexts, err := j.Schedule.Next(now, 1); err == nil && len(nexts) > 0 {
			return "~" + FormatRelativeTime(nexts[0], now)
		}
	}
	return "-"
}

func (m Model) renderInspectorPane(lm layoutMetrics) string {
	focused := m.focus == focusInspector
	paneStyle := stylePane
	titleStyle := stylePaneTitle
	if focused {
		paneStyle = stylePaneFocused
		titleStyle = stylePaneTitleFocused
	}

	contentW := max(20, lm.inspectorInnerW-2)

	detailsTab := styleTabInactive.Render("Details (d)")
	logsTab := styleTabInactive.Render("Logs (L)")
	if m.inspectorTab == tabDetails {
		detailsTab = styleTabActive.Render("Details (d)")
	} else {
		logsTab = styleTabActive.Render("Logs (L)")
	}

	tabBar := titleStyle.Render("[3] ") + detailsTab + " " + logsTab
	if m.inspectorTab == tabLogs {
		followBadge := styleMuted.Render("follow: off (f)")
		if m.logFollow {
			followBadge = styleStatusOK.Render("● follow: on (f)")
		}
		scrollPct := styleMuted.Render(fmt.Sprintf("%3.0f%%", m.logViewport.ScrollPercent()*100))
		tabBar += "  " + followBadge + "  " + scrollPct
	}

	var body string
	if m.inspectorTab == tabDetails {
		body = m.renderDetailsContent(contentW, max(1, lm.inspectorInnerH-2))
	} else {
		body = m.logViewport.View()
	}

	content := lipgloss.JoinVertical(lipgloss.Left, tabBar, body)
	return paneStyle.
		Width(lm.inspectorInnerW).
		Height(lm.inspectorInnerH).
		Render(content)
}

func (m Model) renderDetailsContent(width, maxLines int) string {
	lines := make([]string, 0, maxLines+4)

	// Show repo config error banner if applicable.
	if repo, ok := m.selectedRepo(); ok && repo.HasError() {
		errText := repo.ConfigError
		if errText == "" {
			errText = repo.DaemonError
		}
		lines = append(lines, styleErrorBox.Width(width).Render("Config Error: "+truncateStr(errText, max(10, width-16))))
	}

	job, ok := m.selectedJob()
	if !ok {
		if len(lines) == 0 {
			lines = append(lines, styleMuted.Render("Select a job to inspect its schedule, command, environment, and execution history."))
		}
		return strings.Join(lines[:min(len(lines), maxLines)], "\n")
	}

	kv := func(label, val string) string {
		valW := max(10, width-14)
		return styleDetailLabel.Render(label) + " " + styleDetailValue.Render(truncateStr(val, valW))
	}

	schedLine := job.ScheduleRaw
	if job.ScheduleSummary != "" && job.ScheduleSummary != job.ScheduleRaw {
		schedLine = fmt.Sprintf("%s  →  %s (%s)", job.ScheduleRaw, job.ScheduleSummary, job.ScheduleKind)
	} else if job.ScheduleKind != "" {
		schedLine = fmt.Sprintf("%s (%s)", job.ScheduleSummary, job.ScheduleKind)
	}

	lines = append(lines, kv("Job:", fmt.Sprintf("%s / %s", job.RepoName, job.Name)))
	if job.Description != "" {
		lines = append(lines, kv("Description:", job.Description))
	}
	lines = append(lines, kv("Schedule:", schedLine))
	if job.Command != "" {
		lines = append(lines, kv("Command:", job.Command))
	}
	if job.WorkDir != "" {
		lines = append(lines, kv("WorkDir:", job.WorkDir))
	}

	metaParts := []string{}
	if job.Shell != "" {
		metaParts = append(metaParts, "shell="+job.Shell)
	}
	if job.Timeout > 0 {
		metaParts = append(metaParts, "timeout="+job.Timeout.String())
	}
	if job.Overlap != "" {
		metaParts = append(metaParts, "overlap="+string(job.Overlap))
	}
	metaParts = append(metaParts, fmt.Sprintf("enabled=%t", job.Enabled))
	lines = append(lines, kv("Settings:", strings.Join(metaParts, "  •  ")))

	// Last run telemetry
	if job.LastRun != nil {
		lastInfo := fmt.Sprintf("%s (%s)", job.LastRun.Local().Format("2006-01-02 15:04:05"), FormatRelativeTime(*job.LastRun, m.now()))
		if job.LastDurationMs > 0 {
			lastInfo += " • " + FormatDurationMs(job.LastDurationMs)
		}
		if job.LastExitCode != nil {
			lastInfo += fmt.Sprintf(" • exit %d", *job.LastExitCode)
		}
		if job.RunCount > 0 {
			lastInfo += fmt.Sprintf(" • %d run(s)", job.RunCount)
		}
		lines = append(lines, kv("Last Run:", lastInfo))
	} else {
		lines = append(lines, kv("Last Run:", "never"))
	}

	if job.LastError != "" {
		lines = append(lines, styleDetailLabel.Render("Last Error:")+" "+styleStatusFailed.Render(truncateStr(job.LastError, max(10, width-14))))
	}

	// Upcoming schedule preview
	if job.Schedule != nil && job.Enabled {
		if job.Schedule.Kind == schedule.KindReboot {
			bootInfo := "on next OS boot / daemon startup"
			if job.Schedule.RebootDelay > 0 {
				bootInfo = fmt.Sprintf("%s after OS boot / daemon startup", job.Schedule.RebootDelay)
			}
			if job.LastBootID != "" && m.snap.BootID != "" && job.LastBootID == m.snap.BootID {
				bootInfo = "already ran during this OS boot"
			} else if m.snap.DaemonAlive && job.NextRun != nil {
				bootInfo = fmt.Sprintf("%s (%s)", job.NextRun.Local().Format("Mon 15:04:05"), FormatRelativeTime(*job.NextRun, m.now()))
			}
			lines = append(lines, kv("Upcoming:", bootInfo))
		} else if nexts, err := job.Schedule.Next(m.now(), 3); err == nil && len(nexts) > 0 {
			formatted := make([]string, len(nexts))
			for i, t := range nexts {
				formatted[i] = t.Local().Format("Mon 15:04")
			}
			lines = append(lines, kv("Upcoming:", strings.Join(formatted, "  •  ")))
		}
	}

	if len(job.Env) > 0 {
		keys := make([]string, 0, len(job.Env))
		for k := range job.Env {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		pairs := make([]string, 0, len(keys))
		for _, k := range keys {
			pairs = append(pairs, fmt.Sprintf("%s=%s", k, job.Env[k]))
		}
		lines = append(lines, kv("Env:", strings.Join(pairs, ", ")))
	}

	if job.LogPath != "" {
		lines = append(lines, kv("Log File:", job.LogPath))
	}

	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	return strings.Join(lines, "\n")
}

func (m Model) renderFooter() string {
	if msg, kind, ok := m.activeToast(); ok {
		avail := max(10, m.width-2)
		text := truncateStr(msg, avail)
		switch kind {
		case toastSuccess:
			return styleToastSuccess.Width(m.width).Render(text)
		case toastError:
			return styleToastError.Width(m.width).Render(text)
		default:
			return styleToastInfo.Width(m.width).Render(text)
		}
	}

	hints := []string{
		styleKey.Render("↑↓") + " nav",
		styleKey.Render("tab") + " pane",
		styleKey.Render("t") + " trigger",
		styleKey.Render("L") + " logs",
		styleKey.Render("/") + " filter",
		styleKey.Render("a") + " add",
		styleKey.Render("u") + " remove",
		styleKey.Render("e") + " edit",
		styleKey.Render("v") + " validate",
		styleKey.Render("s") + " daemon",
		styleKey.Render("r") + " refresh",
		styleKey.Render("?") + " help",
		styleKey.Render("q") + " quit",
	}

	if m.filtering {
		hints = []string{
			styleKey.Render("type") + " filter query",
			styleKey.Render("enter") + " apply",
			styleKey.Render("esc") + " clear & close",
		}
	} else if m.focus == focusInspector && m.inspectorTab == tabLogs {
		hints = []string{
			styleKey.Render("↑↓/pgup/pgdn") + " scroll",
			styleKey.Render("g/G") + " top/bottom",
			styleKey.Render("f") + " toggle follow",
			styleKey.Render("d") + " details",
			styleKey.Render("t") + " trigger job",
			styleKey.Render("tab") + " pane",
			styleKey.Render("?") + " help",
			styleKey.Render("q") + " quit",
		}
	}

	line := strings.Join(hints, "  ")
	return styleFooter.Width(m.width).Render(truncateStr(line, max(10, m.width-2)))
}

func (m Model) renderModalOverlay() string {
	var box string
	switch m.modal {
	case modalHelp:
		box = m.renderHelpModal()
	case modalAddRepo:
		box = m.renderAddRepoModal()
	case modalConfirmUnregister:
		box = m.renderConfirmUnregisterModal()
	case modalDaemon:
		box = m.renderDaemonModal()
	}
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

func (m Model) renderHelpModal() string {
	content := strings.Join([]string{
		styleAccent.Render("dcron TUI — Keyboard Shortcuts"),
		"",
		stylePaneTitleFocused.Render("Navigation"),
		"  ↑/k, ↓/j       Move selection / scroll logs",
		"  g / G          Jump to top / bottom",
		"  Tab / S-Tab    Cycle panes (Repos → Jobs → Inspector)",
		"  1 / 2 / 3      Focus Repositories / Jobs / Inspector",
		"  h / l, ← / →   Switch pane or Inspector tab",
		"  /              Filter jobs by name, repo, schedule, or command",
		"",
		stylePaneTitleFocused.Render("Job & Log Controls"),
		"  t / x / Enter  Manually trigger selected job (via daemon if running)",
		"  c              Cancel an active local manual run",
		"  d / L          Switch Inspector to Details / Logs",
		"  f              Toggle live log auto-follow",
		"  r              Refresh state and logs immediately",
		"",
		stylePaneTitleFocused.Render("Repository & Daemon"),
		"  a              Register a repository (Ctrl+N creates starter .cron.toml)",
		"  u              Unregister selected repository",
		"  e              Open selected .cron.toml or global.toml in $EDITOR",
		"  v              Validate selected .cron.toml or global.toml",
		"  s              Daemon & OS background service controls",
		"  ? / q          Toggle help / Quit",
		"",
		styleMuted.Render("Press Esc, Enter, or ? to close."),
	}, "\n")

	modalW := min(74, max(44, m.width-8))
	return styleModal.Width(modalW).Render(content)
}

func (m Model) renderAddRepoModal() string {
	pathLabel := styleMuted.Render("Repository Path:")
	nameLabel := styleMuted.Render("Alias Name (optional):")
	if m.addField == 0 {
		pathLabel = styleAccent.Render("▸ Repository Path:")
	} else {
		nameLabel = styleAccent.Render("▸ Alias Name (optional):")
	}

	parts := []string{
		styleAccent.Render("Register Repository"),
		"",
		pathLabel,
		m.addPathInput.View(),
		"",
		nameLabel,
		m.addNameInput.View(),
	}
	if m.addErr != "" {
		parts = append(parts, "", styleStatusFailed.Render("Error: "+m.addErr))
	}
	parts = append(parts,
		"",
		styleMuted.Render("tab switch field • enter register • ctrl+n init .cron.toml & register • esc cancel"),
	)

	modalW := min(68, max(44, m.width-8))
	return styleModal.Width(modalW).Render(strings.Join(parts, "\n"))
}

func (m Model) renderConfirmUnregisterModal() string {
	parts := []string{
		styleStatusFailed.Render("Unregister Repository?"),
		"",
		fmt.Sprintf("Name: %s", m.unregisterTarget.Entry.Name),
		fmt.Sprintf("Path: %s", m.unregisterTarget.Entry.Path),
		"",
		styleMuted.Render("This removes the repo from dcron's registry (it does not delete .cron.toml)."),
		"",
		styleKey.Render("y / enter") + " confirm    " + styleKey.Render("n / esc") + " cancel",
	}
	modalW := min(66, max(42, m.width-8))
	return styleModal.Width(modalW).Render(strings.Join(parts, "\n"))
}

func (m Model) renderDaemonModal() string {
	statusLine := styleBadgeStopped.Render("○ Stopped")
	if m.snap.DaemonAlive {
		uptime := formatCompactDuration(max(0, m.now().Sub(m.snap.DaemonStartedAt)))
		statusLine = styleBadgeRunning.Render(fmt.Sprintf("● Running (PID %d, uptime %s)", m.snap.DaemonPID, uptime))
	}

	parts := []string{
		styleAccent.Render("Daemon & Service Manager"),
		"",
		"Process: " + statusLine,
	}
	if m.snap.BootID != "" {
		parts = append(parts, "Boot ID: "+styleMuted.Render(m.snap.BootID))
	}
	if m.deps.ServiceManager != nil {
		parts = append(parts, "Service: "+styleMuted.Render(m.deps.ServiceManager.UnitPath()))
	}

	parts = append(parts, "")
	if m.daemonBusy {
		parts = append(parts, styleStatusRunning.Render(m.spinner.View()+" Checking OS service status..."))
	} else if m.daemonStatusErr != "" {
		parts = append(parts, styleStatusFailed.Render("Service status: "+m.daemonStatusErr))
	} else if m.daemonStatusText != "" {
		statusLines := strings.Split(m.daemonStatusText, "\n")
		if len(statusLines) > 10 {
			statusLines = statusLines[:10]
			statusLines = append(statusLines, "...")
		}
		parts = append(parts, styleMuted.Render(strings.Join(statusLines, "\n")))
	} else {
		parts = append(parts, styleMuted.Render("No OS service status output available."))
	}

	parts = append(parts,
		"",
		styleKey.Render("i")+" install/start service  "+
			styleKey.Render("U")+" uninstall service  "+
			styleKey.Render("r")+" refresh  "+
			styleKey.Render("esc")+" close",
	)

	modalW := min(76, max(46, m.width-8))
	return styleModal.Width(modalW).Render(strings.Join(parts, "\n"))
}

func truncateStr(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= maxWidth {
		return s
	}
	if maxWidth == 1 {
		return "…"
	}
	runes := []rune(s)
	out := make([]rune, 0, len(runes))
	w := 0
	for _, r := range runes {
		rw := lipgloss.Width(string(r))
		if w+rw > maxWidth-1 {
			break
		}
		out = append(out, r)
		w += rw
	}
	return string(out) + "…"
}

func padRight(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

func padLeft(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return s
	}
	return strings.Repeat(" ", width-w) + s
}
