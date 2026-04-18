package tui

import (
	"os"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/pksorensen/vibecast/internal/styles"
	"github.com/pksorensen/vibecast/internal/types"
)

// Model is the Bubble Tea model for the TUI.
type Model struct {
	Phase        types.Phase
	SplashFrame  int
	SplashDone   bool
	TransFrame   int
	TransDone    bool
	Spinner      spinner.Model
	SessionID    string
	BroadcastID  string
	StreamURL    string
	PinCode      string
	ViewerCount  int
	TtydPID      int
	TtydPort     int
	TmuxSession  string
	Err          error
	Width        int
	Height       int
	MenuIndex    int
	Uptime       time.Duration
	StartTime    time.Time
	ChatMessages []types.ChatMsg
	ShowChat     bool
	// Settings
	SettingsIndex    int
	PromptSharing    bool
	ShareProjectInfo bool
	ProjectName      string
	// Resume
	ResumeSessionID string
	ResumeMode      bool
	ClaudeResumeID  string // prior session ID to pass to claude --resume (set from VIBECAST_RESUME_SESSION_ID)
	// Claude session
	ClaudeSessionID string
	// Metadata channel
	MetaCh chan []byte
	// Shared status
	Status *types.SharedStatus
	// Session picker
	ClaudeSessions []types.ClaudeSessionInfo
	SessionIndex   int
	SessionScroll  int
	// Image approvals
	PendingImages    []types.PendingImage
	ImageApprovals   int
	PhaseApprovals   bool
	ApprovalIndex    int
	AutoApproveImages bool
	// Multi-pane
	Panes         []types.PaneInfo
	ActivePaneIdx int
}

// InitialModel creates the initial TUI model.
func InitialModel(status *types.SharedStatus) Model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(styles.AccentColor)

	return Model{
		Phase:             types.PhaseSplash,
		Spinner:           s,
		TmuxSession:       "vibecast",
		ShowChat:          true,
		PromptSharing:     true,
		ShareProjectInfo:  true,
		Status:            status,
		AutoApproveImages: os.Getenv("VIBECAST_AUTO_APPROVE_IMAGES") == "1",
	}
}

// WaitingModel creates a minimal model that waits for fkeybar to drive actions.
func WaitingModel(status *types.SharedStatus) Model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(styles.AccentColor)

	return Model{
		Phase:            types.PhaseWaiting,
		Spinner:          s,
		TmuxSession:      "vibecast",
		ShowChat:         true,
		PromptSharing:    true,
		ShareProjectInfo: true,
		Status:           status,
	}
}

// Init returns the initial commands for the model.
func (m Model) Init() tea.Cmd {
	if m.Phase == types.PhaseWaiting {
		return m.Spinner.Tick
	}
	return tea.Batch(
		m.Spinner.Tick,
		SplashTick(),
	)
}
