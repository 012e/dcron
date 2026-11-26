package daemon

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/012e/dcron/internal/state"
)

const launchdLabel = "com.012e.dcron"

// LaunchdManager manages a macOS launchd LaunchAgent for dcron.
type LaunchdManager struct {
	plistDir string
	label    string
	logPath  string
	uid      int
	exec     CommandExecutor
}

// NewLaunchdManager constructs a LaunchdManager writing to
// ~/Library/LaunchAgents/com.012e.dcron.plist.
func NewLaunchdManager(executor CommandExecutor) (*LaunchdManager, error) {
	if executor == nil {
		executor = OSCommandExecutor{}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home directory: %w", err)
	}
	logPath := filepath.Join(home, "Library", "Logs", "dcron.log")
	if stateDir, err := state.DefaultStateDir(); err == nil && stateDir != "" {
		logPath = filepath.Join(stateDir, "daemon.log")
	}
	return &LaunchdManager{
		plistDir: filepath.Join(home, "Library", "LaunchAgents"),
		label:    launchdLabel,
		logPath:  logPath,
		uid:      os.Getuid(),
		exec:     executor,
	}, nil
}

// NewLaunchdManagerAt creates a LaunchdManager targeting an explicit plist directory
// and log path (useful in tests).
func NewLaunchdManagerAt(plistDir, logPath string, uid int, executor CommandExecutor) *LaunchdManager {
	if executor == nil {
		executor = OSCommandExecutor{}
	}
	return &LaunchdManager{
		plistDir: plistDir,
		label:    launchdLabel,
		logPath:  logPath,
		uid:      uid,
		exec:     executor,
	}
}

func (m *LaunchdManager) UnitPath() string {
	return filepath.Join(m.plistDir, m.label+".plist")
}

func (m *LaunchdManager) domain() string {
	return "gui/" + strconv.Itoa(m.uid)
}

func (m *LaunchdManager) serviceTarget() string {
	return m.domain() + "/" + m.label
}

// RenderPlist renders the macOS launchd LaunchAgent property list for binaryPath.
func (m *LaunchdManager) RenderPlist(binaryPath string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n")
	b.WriteString("<dict>\n")
	b.WriteString("\t<key>Label</key>\n")
	b.WriteString("\t<string>" + xmlEscape(m.label) + "</string>\n")
	b.WriteString("\t<key>ProgramArguments</key>\n")
	b.WriteString("\t<array>\n")
	b.WriteString("\t\t<string>" + xmlEscape(binaryPath) + "</string>\n")
	b.WriteString("\t\t<string>daemon</string>\n")
	b.WriteString("\t\t<string>run</string>\n")
	b.WriteString("\t</array>\n")
	b.WriteString("\t<key>RunAtLoad</key>\n")
	b.WriteString("\t<true/>\n")
	b.WriteString("\t<key>KeepAlive</key>\n")
	b.WriteString("\t<dict>\n")
	b.WriteString("\t\t<key>SuccessfulExit</key>\n")
	b.WriteString("\t\t<false/>\n")
	b.WriteString("\t</dict>\n")
	b.WriteString("\t<key>ThrottleInterval</key>\n")
	b.WriteString("\t<integer>3</integer>\n")
	if m.logPath != "" {
		b.WriteString("\t<key>StandardOutPath</key>\n")
		b.WriteString("\t<string>" + xmlEscape(m.logPath) + "</string>\n")
		b.WriteString("\t<key>StandardErrorPath</key>\n")
		b.WriteString("\t<string>" + xmlEscape(m.logPath) + "</string>\n")
	}
	b.WriteString("</dict>\n")
	b.WriteString("</plist>\n")
	return b.String()
}

// Install writes the LaunchAgent plist and loads+starts it via launchctl.
func (m *LaunchdManager) Install(ctx context.Context, binaryPath string) (string, error) {
	absBin, err := filepath.Abs(binaryPath)
	if err != nil {
		return "", fmt.Errorf("resolve binary path: %w", err)
	}
	if m.logPath != "" {
		if err := os.MkdirAll(filepath.Dir(m.logPath), 0o755); err != nil {
			return "", fmt.Errorf("create daemon log dir: %w", err)
		}
	}

	plistPath := m.UnitPath()
	if err := writeFileAtomic(plistPath, []byte(m.RenderPlist(absBin)), 0o644); err != nil {
		return "", fmt.Errorf("write launchd plist %s: %w", plistPath, err)
	}

	// Best-effort unload any previous registration before bootstrapping.
	_, _ = m.exec.Run(ctx, "launchctl", "bootout", m.domain(), plistPath)

	if out, err := m.exec.Run(ctx, "launchctl", "bootstrap", m.domain(), plistPath); err != nil {
		return plistPath, fmt.Errorf("launchctl bootstrap failed: %w (%s)", err, strings.TrimSpace(out))
	}
	return plistPath, nil
}

// Uninstall stops and unloads the LaunchAgent and removes its plist file.
func (m *LaunchdManager) Uninstall(ctx context.Context) error {
	_, _ = m.exec.Run(ctx, "launchctl", "bootout", m.domain(), m.UnitPath())
	if err := os.Remove(m.UnitPath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove launchd plist %s: %w", m.UnitPath(), err)
	}
	return nil
}

// Status returns launchctl status output for the dcron LaunchAgent.
func (m *LaunchdManager) Status(ctx context.Context) (string, error) {
	out, err := m.exec.Run(ctx, "launchctl", "print", m.serviceTarget())
	return strings.TrimSpace(out), err
}

func xmlEscape(s string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}
