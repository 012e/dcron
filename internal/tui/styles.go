package tui

import "github.com/charmbracelet/lipgloss"

var (
	// Palette using adaptive colors for both dark and light terminals.
	colorAccent     = lipgloss.AdaptiveColor{Light: "#4F46E5", Dark: "#818CF8"}
	colorAccentSoft = lipgloss.AdaptiveColor{Light: "#EEF2FF", Dark: "#1E1B4B"}
	colorCyan       = lipgloss.AdaptiveColor{Light: "#0891B2", Dark: "#22D3EE"}
	colorGreen      = lipgloss.AdaptiveColor{Light: "#15803D", Dark: "#4ADE80"}
	colorRed        = lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#F87171"}
	colorYellow     = lipgloss.AdaptiveColor{Light: "#A16207", Dark: "#FACC15"}
	colorMuted      = lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#9CA3AF"}
	colorSubtle     = lipgloss.AdaptiveColor{Light: "#D1D5DB", Dark: "#374151"}
	colorText       = lipgloss.AdaptiveColor{Light: "#111827", Dark: "#F3F4F6"}
	colorSelectedBg = lipgloss.AdaptiveColor{Light: "#E0E7FF", Dark: "#312E81"}
	colorSelectedFg = lipgloss.AdaptiveColor{Light: "#1E1B4B", Dark: "#FFFFFF"}
	colorHeaderBg   = lipgloss.AdaptiveColor{Light: "#F3F4F6", Dark: "#111827"}
	colorErrorBg    = lipgloss.AdaptiveColor{Light: "#FEE2E2", Dark: "#450A0A"}
	colorSuccessBg  = lipgloss.AdaptiveColor{Light: "#DCFCE7", Dark: "#052E16"}
	colorInfoBg     = lipgloss.AdaptiveColor{Light: "#E0F2FE", Dark: "#082F49"}

	styleTitle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(colorAccent).
			Padding(0, 1)

	styleHeaderBar = lipgloss.NewStyle().
			Background(colorHeaderBg).
			Foreground(colorText).
			Padding(0, 1)

	styleBadgeRunning = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorGreen)

	styleBadgeStopped = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorYellow)

	styleMetricLabel = lipgloss.NewStyle().
				Foreground(colorMuted)

	styleMetricValue = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorText)

	styleMetricError = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorRed)

	stylePane = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorSubtle).
			Padding(0, 1)

	stylePaneFocused = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorAccent).
				Padding(0, 1)

	stylePaneTitle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorMuted)

	stylePaneTitleFocused = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorAccent)

	styleTableHeader = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorMuted).
				BorderBottom(true).
				BorderStyle(lipgloss.NormalBorder()).
				BorderForeground(colorSubtle)

	styleRowSelected = lipgloss.NewStyle().
				Background(colorSelectedBg).
				Foreground(colorSelectedFg).
				Bold(true)

	styleRowSelectedDim = lipgloss.NewStyle().
				Background(colorAccentSoft).
				Foreground(colorText)

	styleStatusOK = lipgloss.NewStyle().
			Foreground(colorGreen).
			Bold(true)

	styleStatusRunning = lipgloss.NewStyle().
				Foreground(colorCyan).
				Bold(true)

	styleStatusFailed = lipgloss.NewStyle().
				Foreground(colorRed).
				Bold(true)

	styleStatusIdle = lipgloss.NewStyle().
			Foreground(colorMuted)

	styleStatusDisabled = lipgloss.NewStyle().
				Foreground(colorMuted).
				Faint(true)

	styleMuted = lipgloss.NewStyle().
			Foreground(colorMuted)

	styleAccent = lipgloss.NewStyle().
			Foreground(colorAccent).
			Bold(true)

	styleCyan = lipgloss.NewStyle().
			Foreground(colorCyan)

	styleKey = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorAccent)

	styleDetailLabel = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorMuted).
				Width(13)

	styleDetailValue = lipgloss.NewStyle().
				Foreground(colorText)

	styleTabActive = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorSelectedFg).
			Background(colorAccent).
			Padding(0, 1)

	styleTabInactive = lipgloss.NewStyle().
				Foreground(colorMuted).
				Padding(0, 1)

	styleErrorBox = lipgloss.NewStyle().
			Foreground(colorRed).
			Background(colorErrorBg).
			Padding(0, 1)

	styleToastSuccess = lipgloss.NewStyle().
				Foreground(colorGreen).
				Background(colorSuccessBg).
				Bold(true).
				Padding(0, 1)

	styleToastError = lipgloss.NewStyle().
			Foreground(colorRed).
			Background(colorErrorBg).
			Bold(true).
			Padding(0, 1)

	styleToastInfo = lipgloss.NewStyle().
			Foreground(colorCyan).
			Background(colorInfoBg).
			Bold(true).
			Padding(0, 1)

	styleFooter = lipgloss.NewStyle().
			Foreground(colorMuted).
			Padding(0, 1)

	styleModal = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorAccent).
			Padding(1, 2)

	styleLogStart = lipgloss.NewStyle().
			Foreground(colorCyan).
			Bold(true)

	styleLogEndOK = lipgloss.NewStyle().
			Foreground(colorGreen).
			Bold(true)

	styleLogEndErr = lipgloss.NewStyle().
			Foreground(colorRed).
			Bold(true)
)
