package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/pksorensen/vibecast/internal/types"
)

// SplashTick returns a Cmd that sends a SplashTickMsg after 60ms.
func SplashTick() tea.Cmd {
	return tea.Tick(60*time.Millisecond, func(t time.Time) tea.Msg {
		return types.SplashTickMsg{}
	})
}

// TransTick returns a Cmd that sends a TransTickMsg after 40ms.
func TransTick() tea.Cmd {
	return tea.Tick(40*time.Millisecond, func(t time.Time) tea.Msg {
		return types.TransTickMsg{}
	})
}

// UptimeTick returns a Cmd that sends an UptimeTickMsg after 1 second.
func UptimeTick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return types.UptimeTickMsg{}
	})
}
