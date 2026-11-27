package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/012e/dcron/internal/config"
	"github.com/012e/dcron/internal/daemon"
	"github.com/012e/dcron/internal/runner"
	"github.com/012e/dcron/internal/state"
)

const (
	defaultRefreshInterval = 2 * time.Second
	defaultLogTailLines    = 400
	toastDuration          = 5 * time.Second
)

type paneFocus int

const (
	focusRepos paneFocus = iota
	focusJobs
	focusInspector
)

type inspectorTab int

const (
	tabDetails inspectorTab = iota
	tabLogs
)

type modalKind int

const (
	modalNone modalKind = iota
	modalHelp
	modalAddRepo
	modalConfirmUnregister
	modalDaemon
)

type toastKind int

const (
	toastInfo toastKind = iota
	toastSuccess
	toastError
)

// Messages
type tickMsg time.Time

type snapshotMsg struct {
	snap Snapshot
	err  error
}

type logLoadedMsg struct {
	jobKey  string
	path    string
	content string
	exists  bool
	err     error
}

type jobRunFinishedMsg struct {
	jobKey   string
	repoName string
	jobName  string
	res      runner.Result
}

type jobTriggeredMsg struct {
	job      JobItem
	resp     daemon.TriggerResponse
	err      error
	fallback bool
}

type editorFinishedMsg struct {
	cfgPath string
	err     error
}

type daemonStatusMsg struct {
	output string
	err    error
}

type daemonActionMsg struct {
	message string
	err     error
}

// Model is the Bubble Tea model for the dcron interactive dashboard.
type Model struct {
	deps Deps
	now  func() time.Time

	width  int
	height int
	ready  bool

	snap    Snapshot
	loadErr string

	focus        paneFocus
	inspectorTab inspectorTab

	// Selection state (repoIdx == 0 is "All Repositories"; 1..N are snap.Repos[i-1]).
	repoIdx          int
	selectedRepoPath string
	jobIdx           int
	jobScrollOffset  int
	selectedJobKey   string

	// Filter state
	filterInput textinput.Model
	filtering   bool
	filterQuery string

	// Log viewer state
	logViewport viewport.Model
	logJobKey   string
	logPath     string
	logRaw      string
	logExists   bool
	logFollow   bool
	logErr      string

	// Spinner and active manual runs
	spinner       spinner.Model
	manualRuns    map[string]context.CancelFunc
	manualResults map[string]runner.Result

	// Modals
	modal modalKind

	// Add-repo modal
	addPathInput textinput.Model
	addNameInput textinput.Model
	addField     int
	addErr       string

	// Unregister confirmation target
	unregisterTarget RepoItem

	// Daemon modal
	daemonStatusText string
	daemonStatusErr  string
	daemonBusy       bool

	// Flash toast notification
	toastMsg   string
	toastKind  toastKind
	toastUntil time.Time
}

// NewModel creates an initialized TUI Model wired with deps.
func NewModel(deps Deps) Model {
	if deps.RefreshInterval <= 0 {
		deps.RefreshInterval = defaultRefreshInterval
	}
	if deps.WorkDir == nil {
		deps.WorkDir = os.Getwd
	}

	fi := textinput.New()
	fi.Placeholder = "filter jobs (name, repo, schedule, command)..."
	fi.Prompt = "/ "
	fi.CharLimit = 128

	pathIn := textinput.New()
	pathIn.Placeholder = "/path/to/repo"
	pathIn.Prompt = ""
	pathIn.CharLimit = 512

	nameIn := textinput.New()
	nameIn.Placeholder = "optional alias (defaults to directory name)"
	nameIn.Prompt = ""
	nameIn.CharLimit = 64

	sp := spinner.New()
	sp.Spinner = spinner.MiniDot
	sp.Style = styleStatusRunning

	vp := viewport.New(60, 10)
	vp.MouseWheelEnabled = true

	m := Model{
		deps:          deps,
		now:           time.Now,
		focus:         focusJobs,
		inspectorTab:  tabDetails,
		filterInput:   fi,
		logViewport:   vp,
		logFollow:     true,
		spinner:       sp,
		manualRuns:    make(map[string]context.CancelFunc),
		manualResults: make(map[string]runner.Result),
		addPathInput:  pathIn,
		addNameInput:  nameIn,
	}

	if deps.InitialRepo != "" {
		m.selectedRepoPath = deps.InitialRepo
	}

	return m
}

// Init kicks off the initial snapshot load, refresh ticker, and spinner.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.loadSnapshotCmd(),
		m.tickCmd(),
		m.spinner.Tick,
	)
}

func (m Model) tickCmd() tea.Cmd {
	return tea.Tick(m.deps.RefreshInterval, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m Model) loadSnapshotCmd() tea.Cmd {
	deps := m.deps
	nowFn := m.now
	return func() tea.Msg {
		snap, err := LoadSnapshot(deps, nowFn())
		return snapshotMsg{snap: snap, err: err}
	}
}

func (m Model) loadLogCmd(job JobItem) tea.Cmd {
	key := job.Key()
	path := job.LogPath
	return func() tea.Msg {
		content, exists, err := ReadLogTail(path, defaultLogTailLines)
		return logLoadedMsg{
			jobKey:  key,
			path:    path,
			content: content,
			exists:  exists,
			err:     err,
		}
	}
}

func (m Model) fetchDaemonStatusCmd() tea.Cmd {
	svc := m.deps.ServiceManager
	return func() tea.Msg {
		if svc == nil {
			return daemonStatusMsg{err: errors.New("service manager unavailable on this platform")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := svc.Status(ctx)
		return daemonStatusMsg{output: out, err: err}
	}
}

// Update handles all Bubble Tea messages.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true
		m.resizeComponents()
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case tickMsg:
		cmds := []tea.Cmd{m.tickCmd(), m.loadSnapshotCmd()}
		if m.inspectorTab == tabLogs {
			if job, ok := m.selectedJob(); ok {
				cmds = append(cmds, m.loadLogCmd(job))
			}
		}
		return m, tea.Batch(cmds...)

	case snapshotMsg:
		if msg.err != nil {
			m.loadErr = msg.err.Error()
			return m, nil
		}
		m.loadErr = ""
		m.snap = msg.snap
		m.reconcileSelection()
		var cmd tea.Cmd
		if job, ok := m.selectedJob(); ok {
			if m.inspectorTab == tabLogs || m.logJobKey != job.Key() {
				cmd = m.loadLogCmd(job)
			}
		} else {
			m.clearLogState()
		}
		return m, cmd

	case logLoadedMsg:
		job, ok := m.selectedJob()
		if !ok || job.Key() != msg.jobKey {
			return m, nil
		}
		m.logJobKey = msg.jobKey
		m.logPath = msg.path
		m.logExists = msg.exists
		if msg.err != nil {
			m.logErr = msg.err.Error()
			m.logRaw = ""
		} else {
			m.logErr = ""
			m.logRaw = msg.content
		}
		m.refreshLogViewport()
		return m, nil

	case jobTriggeredMsg:
		if msg.fallback {
			return m.startLocalJobRun(msg.job)
		}
		if msg.err != nil {
			m.setToast(fmt.Sprintf("✗ Trigger %s failed: %v", msg.job.DisplayID(), msg.err), toastError)
			return m, nil
		}
		m.setToast(fmt.Sprintf("▶ Triggered %s in daemon", msg.job.DisplayID()), toastSuccess)
		cmds := []tea.Cmd{m.loadSnapshotCmd()}
		if job, ok := m.selectedJob(); ok && job.Key() == msg.job.Key() {
			cmds = append(cmds, m.loadLogCmd(job))
		}
		return m, tea.Batch(cmds...)

	case jobRunFinishedMsg:
		delete(m.manualRuns, msg.jobKey)
		m.manualResults[msg.jobKey] = msg.res
		dur := msg.res.Duration.Round(time.Millisecond)
		if msg.res.Err != nil {
			m.setToast(fmt.Sprintf("✗ %s/%s failed (exit %d, %s): %v", msg.repoName, msg.jobName, msg.res.ExitCode, dur, msg.res.Err), toastError)
		} else {
			m.setToast(fmt.Sprintf("✓ %s/%s completed in %s", msg.repoName, msg.jobName, dur), toastSuccess)
		}
		cmds := []tea.Cmd{m.loadSnapshotCmd()}
		if job, ok := m.selectedJob(); ok && job.Key() == msg.jobKey {
			cmds = append(cmds, m.loadLogCmd(job))
		}
		return m, tea.Batch(cmds...)

	case editorFinishedMsg:
		if msg.err != nil {
			m.setToast(fmt.Sprintf("Editor error: %v", msg.err), toastError)
		} else {
			m.setToast(fmt.Sprintf("Reloaded after editing %s", filepath.Base(msg.cfgPath)), toastInfo)
		}
		return m, m.loadSnapshotCmd()

	case daemonStatusMsg:
		m.daemonBusy = false
		m.daemonStatusText = msg.output
		if msg.err != nil && msg.output == "" {
			m.daemonStatusErr = msg.err.Error()
		} else {
			m.daemonStatusErr = ""
		}
		return m, nil

	case daemonActionMsg:
		m.daemonBusy = false
		if msg.err != nil {
			m.daemonStatusErr = msg.err.Error()
			m.setToast(fmt.Sprintf("Daemon action failed: %v", msg.err), toastError)
		} else {
			m.daemonStatusErr = ""
			m.setToast(msg.message, toastSuccess)
		}
		return m, tea.Batch(m.loadSnapshotCmd(), m.fetchDaemonStatusCmd())

	case tea.MouseMsg:
		if m.modal != modalNone {
			return m, nil
		}
		return m.handleMouse(msg)

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		if m.focus == focusInspector && m.inspectorTab == tabLogs {
			m.logViewport.ScrollUp(3)
			if !m.logViewport.AtBottom() {
				m.logFollow = false
			}
			return m, nil
		}
		if m.focus == focusRepos {
			return m.moveRepoSelection(-1)
		}
		return m.moveJobSelection(-1)
	case tea.MouseButtonWheelDown:
		if m.focus == focusInspector && m.inspectorTab == tabLogs {
			m.logViewport.ScrollDown(3)
			if m.logViewport.AtBottom() {
				m.logFollow = true
			}
			return m, nil
		}
		if m.focus == focusRepos {
			return m.moveRepoSelection(1)
		}
		return m.moveJobSelection(1)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Always allow Ctrl+C to quit cleanly.
	if key == "ctrl+c" {
		m.cancelAllManualRuns()
		return m, tea.Quit
	}

	// Route keys to active modal first.
	if m.modal != modalNone {
		return m.handleModalKey(msg)
	}

	// Route keys to active filter input.
	if m.filtering {
		switch key {
		case "esc":
			m.filtering = false
			m.filterInput.Blur()
			m.filterInput.SetValue("")
			m.filterQuery = ""
			m.jobIdx = 0
			m.jobScrollOffset = 0
			return m, m.syncSelectedJob()
		case "enter":
			m.filtering = false
			m.filterInput.Blur()
			m.filterQuery = strings.TrimSpace(m.filterInput.Value())
			m.jobIdx = 0
			m.jobScrollOffset = 0
			return m, m.syncSelectedJob()
		default:
			var cmd tea.Cmd
			m.filterInput, cmd = m.filterInput.Update(msg)
			m.filterQuery = strings.TrimSpace(m.filterInput.Value())
			m.jobIdx = 0
			m.jobScrollOffset = 0
			return m, tea.Batch(cmd, m.syncSelectedJob())
		}
	}

	// Global keybindings.
	switch key {
	case "q":
		m.cancelAllManualRuns()
		return m, tea.Quit
	case "?":
		m.modal = modalHelp
		return m, nil
	case "tab":
		m.focus = (m.focus + 1) % 3
		return m, nil
	case "shift+tab":
		m.focus = (m.focus + 2) % 3
		return m, nil
	case "1":
		m.focus = focusRepos
		return m, nil
	case "2":
		m.focus = focusJobs
		return m, nil
	case "3":
		m.focus = focusInspector
		return m, nil
	case "d":
		m.inspectorTab = tabDetails
		return m, nil
	case "L":
		m.inspectorTab = tabLogs
		m.focus = focusInspector
		if job, ok := m.selectedJob(); ok {
			return m, m.loadLogCmd(job)
		}
		return m, nil
	case "f":
		m.logFollow = !m.logFollow
		if m.logFollow {
			m.logViewport.GotoBottom()
			m.setToast("Log auto-follow enabled", toastInfo)
		} else {
			m.setToast("Log auto-follow paused", toastInfo)
		}
		return m, nil
	case "/":
		m.filtering = true
		m.focus = focusJobs
		m.filterInput.SetValue(m.filterQuery)
		m.filterInput.CursorEnd()
		m.filterInput.Focus()
		return m, textinput.Blink
	case "esc":
		if m.filterQuery != "" {
			m.filterQuery = ""
			m.filterInput.SetValue("")
			m.jobIdx = 0
			m.jobScrollOffset = 0
			return m, m.syncSelectedJob()
		}
		return m, nil
	case "r":
		m.setToast("Refreshing...", toastInfo)
		cmds := []tea.Cmd{m.loadSnapshotCmd()}
		if job, ok := m.selectedJob(); ok {
			cmds = append(cmds, m.loadLogCmd(job))
		}
		return m, tea.Batch(cmds...)
	case "t", "x":
		return m.triggerRunSelectedJob()
	case "c":
		return m.cancelSelectedJobRun()
	case "a":
		return m.openAddRepoModal()
	case "u":
		return m.openUnregisterModal()
	case "e":
		return m.openEditorForSelectedRepo()
	case "v":
		return m.validateSelectedRepo()
	case "s":
		m.modal = modalDaemon
		m.daemonBusy = true
		m.daemonStatusErr = ""
		return m, m.fetchDaemonStatusCmd()
	}

	// Pane-specific navigation & actions.
	switch m.focus {
	case focusRepos:
		switch key {
		case "up", "k":
			return m.moveRepoSelection(-1)
		case "down", "j":
			return m.moveRepoSelection(1)
		case "g", "home":
			return m.setRepoIndex(0)
		case "G", "end":
			return m.setRepoIndex(len(m.snap.Repos))
		case "right", "l", "enter":
			m.focus = focusJobs
			return m, nil
		}

	case focusJobs:
		switch key {
		case "up", "k":
			return m.moveJobSelection(-1)
		case "down", "j":
			return m.moveJobSelection(1)
		case "pgup", "ctrl+u":
			return m.moveJobSelection(-5)
		case "pgdown", "ctrl+d":
			return m.moveJobSelection(5)
		case "g", "home":
			return m.setJobIndex(0)
		case "G", "end":
			return m.setJobIndex(len(m.filteredJobs()) - 1)
		case "left", "h":
			m.focus = focusRepos
			return m, nil
		case "right", "l":
			m.focus = focusInspector
			return m, nil
		case "enter":
			return m.triggerRunSelectedJob()
		}

	case focusInspector:
		switch key {
		case "left", "h":
			if m.inspectorTab == tabLogs {
				m.inspectorTab = tabDetails
				return m, nil
			}
			m.focus = focusJobs
			return m, nil
		case "right", "l":
			if m.inspectorTab == tabDetails {
				m.inspectorTab = tabLogs
				if job, ok := m.selectedJob(); ok {
					return m, m.loadLogCmd(job)
				}
			}
			return m, nil
		}
		if m.inspectorTab == tabLogs {
			switch key {
			case "up", "k":
				m.logViewport.ScrollUp(1)
				if !m.logViewport.AtBottom() {
					m.logFollow = false
				}
				return m, nil
			case "down", "j":
				m.logViewport.ScrollDown(1)
				if m.logViewport.AtBottom() {
					m.logFollow = true
				}
				return m, nil
			case "pgup", "ctrl+u":
				m.logViewport.HalfPageUp()
				if !m.logViewport.AtBottom() {
					m.logFollow = false
				}
				return m, nil
			case "pgdown", "ctrl+d":
				m.logViewport.HalfPageDown()
				if m.logViewport.AtBottom() {
					m.logFollow = true
				}
				return m, nil
			case "g", "home":
				m.logViewport.GotoTop()
				m.logFollow = false
				return m, nil
			case "G", "end":
				m.logViewport.GotoBottom()
				m.logFollow = true
				return m, nil
			}
		}
	}

	return m, nil
}

func (m Model) handleModalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	switch m.modal {
	case modalHelp:
		if key == "esc" || key == "?" || key == "q" || key == "enter" {
			m.modal = modalNone
		}
		return m, nil

	case modalConfirmUnregister:
		switch key {
		case "y", "Y", "enter":
			target := m.unregisterTarget
			m.modal = modalNone
			if m.deps.Registry == nil {
				m.setToast("Registry unavailable", toastError)
				return m, nil
			}
			removed, err := m.deps.Registry.Remove(target.Entry.Path)
			if err != nil {
				m.setToast(fmt.Sprintf("Unregister failed: %v", err), toastError)
				return m, nil
			}
			if m.selectedRepoPath == removed.Path {
				m.selectedRepoPath = ""
				m.repoIdx = 0
			}
			m.setToast(fmt.Sprintf("Unregistered %q (%s)", removed.Name, removed.Path), toastSuccess)
			return m, m.loadSnapshotCmd()
		case "n", "N", "esc", "q":
			m.modal = modalNone
			return m, nil
		}
		return m, nil

	case modalAddRepo:
		switch key {
		case "esc":
			m.modal = modalNone
			m.addErr = ""
			m.addPathInput.Blur()
			m.addNameInput.Blur()
			return m, nil
		case "tab", "shift+tab", "up", "down":
			if m.addField == 0 {
				m.addField = 1
				m.addPathInput.Blur()
				m.addNameInput.Focus()
			} else {
				m.addField = 0
				m.addNameInput.Blur()
				m.addPathInput.Focus()
			}
			return m, textinput.Blink
		case "ctrl+n":
			return m.initStarterAndRegister()
		case "enter":
			return m.submitAddRepo()
		default:
			var cmd tea.Cmd
			if m.addField == 0 {
				m.addPathInput, cmd = m.addPathInput.Update(msg)
			} else {
				m.addNameInput, cmd = m.addNameInput.Update(msg)
			}
			return m, cmd
		}

	case modalDaemon:
		switch key {
		case "esc", "q", "s":
			m.modal = modalNone
			return m, nil
		case "r":
			m.daemonBusy = true
			return m, m.fetchDaemonStatusCmd()
		case "i":
			if m.deps.ServiceManager == nil {
				m.daemonStatusErr = "service manager unavailable on this platform"
				return m, nil
			}
			m.daemonBusy = true
			svc := m.deps.ServiceManager
			return m, func() tea.Msg {
				bin, err := os.Executable()
				if err != nil {
					return daemonActionMsg{err: fmt.Errorf("locate dcron binary: %w", err)}
				}
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				unitPath, err := svc.Install(ctx, bin)
				if err != nil {
					return daemonActionMsg{err: err}
				}
				return daemonActionMsg{message: fmt.Sprintf("Installed and started service at %s", unitPath)}
			}
		case "U":
			if m.deps.ServiceManager == nil {
				m.daemonStatusErr = "service manager unavailable on this platform"
				return m, nil
			}
			m.daemonBusy = true
			svc := m.deps.ServiceManager
			return m, func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				if err := svc.Uninstall(ctx); err != nil {
					return daemonActionMsg{err: err}
				}
				return daemonActionMsg{message: "Uninstalled dcron background service"}
			}
		}
		return m, nil
	}

	return m, nil
}

func (m Model) openAddRepoModal() (Model, tea.Cmd) {
	m.modal = modalAddRepo
	m.addErr = ""
	m.addField = 0
	cwd := ""
	if m.deps.WorkDir != nil {
		if d, err := m.deps.WorkDir(); err == nil {
			cwd = d
		}
	}
	m.addPathInput.SetValue(cwd)
	m.addPathInput.CursorEnd()
	m.addNameInput.SetValue("")
	m.addPathInput.Focus()
	m.addNameInput.Blur()
	return m, textinput.Blink
}

func (m Model) submitAddRepo() (Model, tea.Cmd) {
	rawPath := strings.TrimSpace(m.addPathInput.Value())
	alias := strings.TrimSpace(m.addNameInput.Value())
	if rawPath == "" {
		m.addErr = "Repository path cannot be empty."
		return m, nil
	}
	rawPath = expandHomePath(rawPath)
	if m.deps.Loader == nil || m.deps.Registry == nil {
		m.addErr = "Registry or config loader unavailable."
		return m, nil
	}

	cfg, err := m.deps.Loader.Load(rawPath)
	if err != nil {
		m.addErr = err.Error()
		return m, nil
	}
	entry, err := m.deps.Registry.Add(cfg.RepoPath, alias)
	if err != nil {
		m.addErr = err.Error()
		return m, nil
	}

	m.modal = modalNone
	m.addErr = ""
	m.addPathInput.Blur()
	m.addNameInput.Blur()
	m.selectedRepoPath = entry.Path
	m.setToast(fmt.Sprintf("Registered %q (%d job(s))", entry.Name, len(cfg.Jobs)), toastSuccess)
	return m, m.loadSnapshotCmd()
}

func (m Model) initStarterAndRegister() (Model, tea.Cmd) {
	rawPath := strings.TrimSpace(m.addPathInput.Value())
	if rawPath == "" {
		m.addErr = "Enter a directory path first."
		return m, nil
	}
	rawPath = expandHomePath(rawPath)
	absPath, err := filepath.Abs(rawPath)
	if err != nil {
		m.addErr = err.Error()
		return m, nil
	}
	info, err := os.Stat(absPath)
	if err != nil || !info.IsDir() {
		m.addErr = fmt.Sprintf("%s is not an existing directory", absPath)
		return m, nil
	}
	cfgPath := filepath.Join(absPath, config.FileName)
	if m.deps.Loader != nil {
		cfgPath = m.deps.Loader.ConfigPath(absPath)
	}
	if _, err := os.Stat(cfgPath); errors.Is(err, os.ErrNotExist) {
		if werr := os.WriteFile(cfgPath, []byte(config.StarterTemplate()), 0o644); werr != nil {
			m.addErr = fmt.Sprintf("write %s: %v", cfgPath, werr)
			return m, nil
		}
	}
	return m.submitAddRepo()
}

func expandHomePath(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			if p == "~" {
				return home
			}
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

func (m Model) openUnregisterModal() (Model, tea.Cmd) {
	repo, ok := m.targetRepoForAction()
	if !ok {
		m.setToast("Select a repository to unregister", toastInfo)
		return m, nil
	}
	if repo.IsGlobal {
		m.setToast("Global crons are auto-managed (delete global.toml to remove)", toastInfo)
		return m, nil
	}
	m.unregisterTarget = repo
	m.modal = modalConfirmUnregister
	return m, nil
}

func (m Model) openEditorForSelectedRepo() (Model, tea.Cmd) {
	repo, ok := m.targetRepoForAction()
	if !ok {
		m.setToast("Select a repository to edit its config", toastInfo)
		return m, nil
	}
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	fields := parseEditorCommand(editor, runtime.GOOS)
	args := append(fields[1:], repo.ConfigPath)
	cmd := exec.Command(fields[0], args...)
	cfgPath := repo.ConfigPath
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
		return editorFinishedMsg{cfgPath: cfgPath, err: err}
	})
}

func parseEditorCommand(editor, goos string) []string {
	fallback := "vi"
	if goos == "windows" {
		fallback = "notepad"
	}
	trimmed := strings.TrimSpace(editor)
	if trimmed == "" {
		return []string{fallback}
	}
	// If the entire value is an existing executable path (e.g. "C:\Program Files\...\editor.exe"),
	// don't split on internal spaces.
	unquoted := strings.Trim(trimmed, `"'`)
	if unquoted != "" {
		if info, err := os.Stat(unquoted); err == nil && !info.IsDir() {
			return []string{unquoted}
		}
	}
	fields := splitCommandArgs(trimmed)
	if len(fields) == 0 {
		return []string{fallback}
	}
	return fields
}

func splitCommandArgs(s string) []string {
	var args []string
	var cur strings.Builder
	var quote rune
	inToken := false

	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote = r
			inToken = true
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if inToken {
				args = append(args, cur.String())
				cur.Reset()
				inToken = false
			}
		default:
			cur.WriteRune(r)
			inToken = true
		}
	}
	if inToken {
		args = append(args, cur.String())
	}
	return args
}

func (m Model) validateSelectedRepo() (Model, tea.Cmd) {
	repo, ok := m.targetRepoForAction()
	if !ok {
		m.setToast("No repository selected to validate", toastInfo)
		return m, nil
	}
	if m.deps.Loader == nil {
		m.setToast("Config loader unavailable", toastError)
		return m, nil
	}
	var (
		cfg *config.RepoConfig
		err error
	)
	if repo.IsGlobal {
		cfg, err = m.deps.Loader.LoadFile(repo.ConfigPath, "")
	} else {
		cfg, err = m.deps.Loader.Load(repo.Entry.Path)
	}
	if err != nil {
		m.setToast(fmt.Sprintf("✗ %s invalid: %v", repo.Entry.Name, err), toastError)
		return m, m.loadSnapshotCmd()
	}
	m.setToast(fmt.Sprintf("✓ %s valid (%d job(s))", repo.Entry.Name, len(cfg.Jobs)), toastSuccess)
	return m, m.loadSnapshotCmd()
}

func (m Model) triggerRunSelectedJob() (Model, tea.Cmd) {
	job, ok := m.selectedJob()
	if !ok {
		m.setToast("No job selected to trigger", toastInfo)
		return m, nil
	}
	key := job.Key()
	if _, alreadyRunning := m.manualRuns[key]; alreadyRunning {
		m.setToast(fmt.Sprintf("%s is already running (press c to cancel)", job.DisplayID()), toastInfo)
		return m, nil
	}

	// Prefer triggering via the running daemon so state, overlap policy, and background execution stay unified.
	if m.snap.DaemonAlive && m.deps.Controller != nil {
		ctrl := m.deps.Controller
		j := job
		m.setToast(fmt.Sprintf("▶ Triggering %s in daemon...", j.DisplayID()), toastInfo)
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			resp, err := ctrl.Trigger(ctx, daemon.TriggerRequest{
				RepoPath: j.RepoPath,
				JobName:  j.Name,
				Wait:     false,
			})
			if errors.Is(err, daemon.ErrDaemonNotRunning) {
				return jobTriggeredMsg{job: j, fallback: true}
			}
			return jobTriggeredMsg{job: j, resp: resp, err: err}
		}
	}

	return m.startLocalJobRun(job)
}

func (m Model) startLocalJobRun(job JobItem) (Model, tea.Cmd) {
	if m.deps.Runner == nil {
		m.setToast("Job runner unavailable", toastError)
		return m, nil
	}
	key := job.Key()
	if _, alreadyRunning := m.manualRuns[key]; alreadyRunning {
		m.setToast(fmt.Sprintf("%s is already running (press c to cancel)", job.DisplayID()), toastInfo)
		return m, nil
	}
	cfgJob, ok := job.ConfigJob()
	if !ok {
		m.setToast(fmt.Sprintf("Cannot trigger %s: fix .cron.toml config first", job.DisplayID()), toastError)
		return m, nil
	}

	runCtx, cancel := context.WithCancel(context.Background())
	m.manualRuns[key] = cancel
	m.setToast(fmt.Sprintf("▶ Triggered %s locally...", job.DisplayID()), toastInfo)

	r := m.deps.Runner
	store := m.deps.StateStore
	checker := m.deps.ProcessChecker
	req := runner.Request{
		RepoName: job.RepoName,
		RepoPath: job.RepoPath,
		Job:      cfgJob,
		LogPath:  job.LogPath,
	}
	repoName := job.RepoName
	repoPath := job.RepoPath
	jobName := job.Name
	logPath := job.LogPath

	return m, func() tea.Msg {
		res := r.Run(runCtx, req)
		cancel()
		persistLocalRunState(store, checker, repoName, repoPath, cfgJob, logPath, res)
		return jobRunFinishedMsg{
			jobKey:   key,
			repoName: repoName,
			jobName:  jobName,
			res:      res,
		}
	}
}

func persistLocalRunState(store state.Store, checker state.ProcessChecker, repoName, repoPath string, job config.Job, logPath string, res runner.Result) {
	if store == nil {
		return
	}
	snap, err := store.Load()
	if err != nil {
		return
	}
	if checker != nil && snap.PID > 0 && checker.IsAlive(snap.PID) {
		return
	}
	if snap.Repos == nil {
		snap.Repos = make(map[string]state.RepoState)
	}
	rs, ok := snap.Repos[repoPath]
	if !ok {
		rs = state.RepoState{
			Name: repoName,
			Path: repoPath,
			Jobs: make(map[string]state.JobState),
		}
	}
	if rs.Jobs == nil {
		rs.Jobs = make(map[string]state.JobState)
	}
	js := rs.Jobs[job.Name]
	js.Name = job.Name
	js.Description = job.Description
	js.ScheduleRaw = job.Schedule.Raw
	js.ScheduleSummary = job.Schedule.Summary()
	js.ScheduleKind = job.Schedule.Kind.String()
	js.Enabled = job.Enabled
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
	if logPath != "" {
		js.LogPath = logPath
	}
	rs.Name = repoName
	rs.Path = repoPath
	rs.Jobs[job.Name] = js
	snap.Repos[repoPath] = rs
	snap.UpdatedAt = time.Now().UTC()
	_ = store.Save(snap)
}

func (m Model) cancelSelectedJobRun() (Model, tea.Cmd) {
	job, ok := m.selectedJob()
	if !ok {
		return m, nil
	}
	cancel, running := m.manualRuns[job.Key()]
	if !running {
		m.setToast("No in-TUI run active for selected job", toastInfo)
		return m, nil
	}
	cancel()
	m.setToast(fmt.Sprintf("Cancelling %s...", job.DisplayID()), toastInfo)
	return m, nil
}

func (m *Model) cancelAllManualRuns() {
	for k, cancel := range m.manualRuns {
		cancel()
		delete(m.manualRuns, k)
	}
}

func (m Model) moveRepoSelection(delta int) (Model, tea.Cmd) {
	return m.setRepoIndex(m.repoIdx + delta)
}

func (m Model) setRepoIndex(idx int) (Model, tea.Cmd) {
	maxIdx := len(m.snap.Repos) // 0 is All, 1..N are individual repos
	if idx < 0 {
		idx = 0
	}
	if idx > maxIdx {
		idx = maxIdx
	}
	if idx == m.repoIdx && (idx == 0 && m.selectedRepoPath == "" || idx > 0 && m.selectedRepoPath == m.snap.Repos[idx-1].Entry.Path) {
		return m, nil
	}
	m.repoIdx = idx
	if idx == 0 {
		m.selectedRepoPath = ""
	} else {
		m.selectedRepoPath = m.snap.Repos[idx-1].Entry.Path
	}
	m.jobIdx = 0
	m.jobScrollOffset = 0
	return m, m.syncSelectedJob()
}

func (m Model) moveJobSelection(delta int) (Model, tea.Cmd) {
	return m.setJobIndex(m.jobIdx + delta)
}

func (m Model) setJobIndex(idx int) (Model, tea.Cmd) {
	jobs := m.filteredJobs()
	if len(jobs) == 0 {
		m.jobIdx = 0
		m.jobScrollOffset = 0
		m.selectedJobKey = ""
		m.clearLogState()
		return m, nil
	}
	if idx < 0 {
		idx = 0
	}
	if idx >= len(jobs) {
		idx = len(jobs) - 1
	}
	m.jobIdx = idx
	return m, m.syncSelectedJob()
}

func (m *Model) syncSelectedJob() tea.Cmd {
	jobs := m.filteredJobs()
	if len(jobs) == 0 {
		m.jobIdx = 0
		m.jobScrollOffset = 0
		m.selectedJobKey = ""
		m.clearLogState()
		return nil
	}
	if m.jobIdx >= len(jobs) {
		m.jobIdx = len(jobs) - 1
	}
	if m.jobIdx < 0 {
		m.jobIdx = 0
	}
	m.ensureJobVisible()
	job := jobs[m.jobIdx]
	if job.Key() != m.selectedJobKey {
		m.selectedJobKey = job.Key()
		m.logFollow = true
		return m.loadLogCmd(job)
	}
	return nil
}

func (m *Model) reconcileSelection() {
	// Reconcile repository selection by path or name (supports InitialRepo).
	if m.selectedRepoPath != "" {
		found := -1
		for i, r := range m.snap.Repos {
			if r.Entry.Path == m.selectedRepoPath || r.Entry.Name == m.selectedRepoPath {
				found = i + 1
				m.selectedRepoPath = r.Entry.Path
				break
			}
		}
		if found >= 0 {
			m.repoIdx = found
		} else {
			m.repoIdx = 0
			m.selectedRepoPath = ""
		}
	} else {
		m.repoIdx = 0
	}

	// Reconcile job selection by stable job key.
	jobs := m.filteredJobs()
	if len(jobs) == 0 {
		m.jobIdx = 0
		m.jobScrollOffset = 0
		m.selectedJobKey = ""
		return
	}
	if m.selectedJobKey != "" {
		for i, j := range jobs {
			if j.Key() == m.selectedJobKey {
				m.jobIdx = i
				m.ensureJobVisible()
				return
			}
		}
	}
	if m.jobIdx >= len(jobs) {
		m.jobIdx = len(jobs) - 1
	}
	if m.jobIdx < 0 {
		m.jobIdx = 0
	}
	m.selectedJobKey = jobs[m.jobIdx].Key()
	m.ensureJobVisible()
}

func (m *Model) clearLogState() {
	m.logJobKey = ""
	m.logPath = ""
	m.logRaw = ""
	m.logExists = false
	m.logErr = ""
	m.logViewport.SetContent("")
}

func (m *Model) setToast(msg string, kind toastKind) {
	m.toastMsg = msg
	m.toastKind = kind
	m.toastUntil = m.now().Add(toastDuration)
}

func (m Model) activeToast() (string, toastKind, bool) {
	if m.toastMsg == "" {
		return "", toastInfo, false
	}
	if !m.toastUntil.IsZero() && m.now().After(m.toastUntil) {
		return "", toastInfo, false
	}
	return m.toastMsg, m.toastKind, true
}

func (m Model) selectedRepo() (RepoItem, bool) {
	if m.repoIdx <= 0 || m.repoIdx > len(m.snap.Repos) {
		return RepoItem{}, false
	}
	return m.snap.Repos[m.repoIdx-1], true
}

func (m Model) targetRepoForAction() (RepoItem, bool) {
	if repo, ok := m.selectedRepo(); ok {
		return repo, true
	}
	if job, ok := m.selectedJob(); ok {
		for _, r := range m.snap.Repos {
			if r.Entry.Path == job.RepoPath {
				return r, true
			}
		}
	}
	if len(m.snap.Repos) == 1 {
		return m.snap.Repos[0], true
	}
	return RepoItem{}, false
}

func (m Model) filteredJobs() []JobItem {
	var base []JobItem
	if repo, ok := m.selectedRepo(); ok {
		base = repo.Jobs
	} else {
		base = m.snap.AllJobs
	}

	q := strings.ToLower(strings.TrimSpace(m.filterQuery))
	if q == "" {
		return base
	}
	out := make([]JobItem, 0, len(base))
	for _, j := range base {
		if strings.Contains(strings.ToLower(j.Name), q) ||
			strings.Contains(strings.ToLower(j.RepoName), q) ||
			strings.Contains(strings.ToLower(j.ScheduleRaw), q) ||
			strings.Contains(strings.ToLower(j.ScheduleSummary), q) ||
			strings.Contains(strings.ToLower(j.Command), q) ||
			strings.Contains(strings.ToLower(j.Description), q) {
			out = append(out, j)
		}
	}
	return out
}

func (m Model) selectedJob() (JobItem, bool) {
	jobs := m.filteredJobs()
	if len(jobs) == 0 || m.jobIdx < 0 || m.jobIdx >= len(jobs) {
		return JobItem{}, false
	}
	return m.enrichJobWithManualResult(jobs[m.jobIdx]), true
}

// enrichJobWithManualResult overlays recent in-TUI manual execution telemetry
// when it is newer than (or absent from) daemon state.
func (m Model) enrichJobWithManualResult(j JobItem) JobItem {
	res, ok := m.manualResults[j.Key()]
	if !ok {
		return j
	}
	if j.LastRun != nil && j.LastRun.After(res.StartedAt) {
		return j
	}
	started := res.StartedAt
	j.LastRun = &started
	j.LastDurationMs = res.Duration.Milliseconds()
	exitCode := res.ExitCode
	j.LastExitCode = &exitCode
	if res.Err != nil {
		j.LastError = res.Err.Error()
	} else {
		j.LastError = ""
	}
	return j
}

func (m Model) isManualRunning(jobKey string) bool {
	_, ok := m.manualRuns[jobKey]
	return ok
}

func (m *Model) resizeComponents() {
	layout := m.computeLayout()
	// Log viewport sits inside inspector pane (padding 0,1 + title/tab row + divider).
	vpW := max(10, layout.inspectorInnerW-2)
	vpH := max(3, layout.inspectorInnerH-2)
	m.logViewport.Width = vpW
	m.logViewport.Height = vpH
	m.filterInput.Width = max(16, layout.jobsInnerW-14)
	m.addPathInput.Width = min(56, max(24, m.width-20))
	m.addNameInput.Width = min(56, max(24, m.width-20))
	m.ensureJobVisible()
	m.refreshLogViewport()
}

func (m *Model) ensureJobVisible() {
	layout := m.computeLayout()
	visibleRows := m.jobsVisibleRows(layout)
	if visibleRows <= 0 {
		return
	}
	if m.jobIdx < m.jobScrollOffset {
		m.jobScrollOffset = m.jobIdx
	}
	if m.jobIdx >= m.jobScrollOffset+visibleRows {
		m.jobScrollOffset = m.jobIdx - visibleRows + 1
	}
	if m.jobScrollOffset < 0 {
		m.jobScrollOffset = 0
	}
}

func (m *Model) refreshLogViewport() {
	if m.logErr != "" {
		m.logViewport.SetContent(styleStatusFailed.Render("Error reading log: " + m.logErr))
		return
	}
	if !m.logExists {
		hint := "No execution logs yet for this job.\nPress t (or Enter in Jobs) to trigger it now."
		if m.logPath != "" {
			hint += "\n\nExpected log file:\n" + m.logPath
		}
		m.logViewport.SetContent(styleMuted.Render(hint))
		return
	}
	if strings.TrimSpace(m.logRaw) == "" {
		m.logViewport.SetContent(styleMuted.Render("(log file is empty)"))
		return
	}
	formatted := formatLogContent(m.logRaw)
	m.logViewport.SetContent(formatted)
	if m.logFollow {
		m.logViewport.GotoBottom()
	}
}

func formatLogContent(raw string) string {
	lines := strings.Split(raw, "\n")
	out := make([]string, len(lines))
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "=== [") && strings.Contains(line, "] START "):
			out[i] = styleLogStart.Render(line)
		case strings.HasPrefix(line, "=== [") && strings.Contains(line, "] END exit=0 "):
			out[i] = styleLogEndOK.Render(line)
		case strings.HasPrefix(line, "=== [") && strings.Contains(line, "] END "):
			out[i] = styleLogEndErr.Render(line)
		default:
			out[i] = line
		}
	}
	return strings.Join(out, "\n")
}
