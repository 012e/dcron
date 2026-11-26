package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
)

// Run starts the interactive Bubble Tea TUI and blocks until the user exits
// or ctx is cancelled.
func Run(ctx context.Context, deps Deps) error {
	model := NewModel(deps)
	p := tea.NewProgram(
		model,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
		tea.WithContext(ctx),
	)
	_, err := p.Run()
	return err
}
