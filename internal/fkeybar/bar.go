package fkeybar

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/pksorensen/vibecast/internal/styles"
	"github.com/pksorensen/vibecast/internal/types"
)

// ── Messages ─────────────────────────────────────────────────────────────────

type statusTickMsg struct{}
type statusUpdatedMsg struct {
	Status *StatusResponse
	Panes  []types.PaneStatus
}
type statusErrorMsg struct{}

type sessionEntry struct {
	ID      string `json:"sessionId"`
	Prompt  string `json:"firstPrompt"`
	Updated string `json:"lastUpdated"`
	Count   int    `json:"messageCount"`
}

// ── Info Model (full-screen info window) ─────────────────────────────────────

// InfoModel runs as the sole process in the "info" tmux window.
// Pre-stream: shows menu (Start Streaming, Resume, Settings, Quit).
// Post-stream: shows live stats (PIN, URL, viewers, uptime).
type InfoModel struct {
	Width    int
	Height   int

	// State from control socket polling
	Phase     string
	SessionID string
	Viewers   int
	Uptime    string
	URL       string
	PinCode   string
	Panes     []types.PaneStatus

	// Menu state (pre-stream)
	MenuIndex int
	menuItems []string
	SubView   string // "", "settings", "sessions"

	// Settings
	PromptSharing    bool
	ShareProjectInfo bool
	SettingsIndex    int

	// Sessions
	Sessions      []sessionEntry
	SessionIndex  int
	SessionScroll int

	client *Client
	VSCode bool
}

// NewInfoModel creates the full-screen info model.
func NewInfoModel(sessionID string) InfoModel {
	return InfoModel{
		SessionID:        sessionID,
		Phase:            "menu",
		menuItems:        []string{"Start Streaming", "Resume Session", "Settings", "Quit"},
		PromptSharing:    true,
		ShareProjectInfo: true,
		client:           NewClient(),
		VSCode:           os.Getenv("TERM_PROGRAM") == "vscode",
	}
}

func (m InfoModel) Init() tea.Cmd {
	return tea.Batch(m.pollStatus(), scheduleStatusTick())
}

func (m InfoModel) pollStatus() tea.Cmd {
	client := m.client
	return func() tea.Msg {
		status, err := client.GetStatus()
		if err != nil {
			return statusErrorMsg{}
		}
		panes, _ := client.GetPanes()
		return statusUpdatedMsg{Status: status, Panes: panes}
	}
}

func (m InfoModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.Width = msg.Width
		m.Height = msg.Height
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg.String())

	case statusTickMsg:
		return m, tea.Batch(m.pollStatus(), scheduleStatusTick())

	case statusUpdatedMsg:
		if msg.Status != nil {
			m.Phase = msg.Status.Phase
			m.Viewers = msg.Status.Viewers
			m.Uptime = msg.Status.Uptime
			if msg.Status.SessionID != "" {
				m.SessionID = msg.Status.SessionID
			}
			if msg.Status.URL != "" {
				m.URL = msg.Status.URL
			}
			if msg.Status.PinCode != "" {
				m.PinCode = msg.Status.PinCode
			}
		}
		if msg.Panes != nil {
			m.Panes = msg.Panes
		}
		return m, nil

	case statusErrorMsg:
		return m, nil
	}
	return m, nil
}

func (m InfoModel) handleKey(key string) (tea.Model, tea.Cmd) {
	// When live, the info screen is read-only (F-keys handled by tmux bindings)
	if m.Phase == "live" || m.Phase == "starting" {
		if key == "q" || key == "ctrl+c" {
			return m, tea.Quit
		}
		return m, nil
	}

	// Pre-stream: menu navigation
	switch m.SubView {
	case "settings":
		return m.handleSettingsKey(key)
	case "sessions":
		return m.handleSessionsKey(key)
	default:
		return m.handleMenuKey(key)
	}
}

func (m InfoModel) handleMenuKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.MenuIndex > 0 {
			m.MenuIndex--
		}
	case "down", "j":
		if m.MenuIndex < len(m.menuItems)-1 {
			m.MenuIndex++
		}
	case "enter", " ":
		switch m.MenuIndex {
		case 0: // Start Streaming
			go m.client.PostStartStream(m.PromptSharing, m.ShareProjectInfo)
			m.Phase = "starting"
		case 1: // Resume Session
			m.SubView = "sessions"
		case 2: // Settings
			m.SubView = "settings"
		case 3: // Quit
			return m, tea.Quit
		}
	case "q", "ctrl+c":
		return m, tea.Quit
	}
	return m, nil
}

func (m InfoModel) handleSettingsKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.SettingsIndex > 0 {
			m.SettingsIndex--
		}
	case "down", "j":
		if m.SettingsIndex < 1 {
			m.SettingsIndex++
		}
	case "enter", " ":
		if m.SettingsIndex == 0 {
			m.PromptSharing = !m.PromptSharing
		} else {
			m.ShareProjectInfo = !m.ShareProjectInfo
		}
	case "esc":
		m.SubView = ""
	}
	return m, nil
}

func (m InfoModel) handleSessionsKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.SessionIndex > 0 {
			m.SessionIndex--
			if m.SessionIndex < m.SessionScroll {
				m.SessionScroll = m.SessionIndex
			}
		}
	case "down", "j":
		if m.SessionIndex < len(m.Sessions)-1 {
			m.SessionIndex++
			if m.SessionIndex >= m.SessionScroll+6 {
				m.SessionScroll = m.SessionIndex - 5
			}
		}
	case "enter":
		// TODO: resume selected session
		m.SubView = ""
	case "esc":
		m.SubView = ""
	}
	return m, nil
}

// ── Info Views ───────────────────────────────────────────────────────────────

func (m InfoModel) View() string {
	if m.Width == 0 || m.Height == 0 {
		return ""
	}

	var content string
	switch {
	case m.Phase == "live":
		content = m.viewLive()
	case m.Phase == "starting":
		content = m.viewStarting()
	case m.SubView == "settings":
		content = m.viewSettings()
	case m.SubView == "sessions":
		content = m.viewSessions()
	default:
		content = m.viewMenu()
	}

	fkeyRow := m.renderInfoFKeyRow()

	// Pin F-key row to the bottom by padding content to fill the terminal height
	contentLines := strings.Count(content, "\n") + 1
	fkeyLines := strings.Count(fkeyRow, "\n") + 1
	padding := m.Height - contentLines - fkeyLines
	if padding < 0 {
		padding = 0
	}

	return content + strings.Repeat("\n", padding) + fkeyRow
}

func (m InfoModel) renderInfoFKeyRow() string {
	r := styles.RenderFKeyEntry
	if m.VSCode {
		return " " + strings.Join([]string{
			r("^b1", " Workspace"),
			r("^b2", " New"),
			r("^b3", " ◀"),
			r("^b4", " ▶"),
			r("^b5", " Close"),
			r("^b6", " Restart"),
			r("^b9", " Help"),
			r("^b0", " Stop"),
		}, " ")
	}
	return " " + strings.Join([]string{
		r("F1", " Workspace"),
		r("F2", " New"),
		r("F3", " ◀"),
		r("F4", " ▶"),
		r("F5", " Close"),
		r("F6", " Restart"),
		r("F9", " Help"),
		r("F10", " Stop"),
	}, " ")
}

func (m InfoModel) viewMenu() string {
	titleStyle := lipgloss.NewStyle().Foreground(styles.AccentColor).Bold(true)
	dimStyle := lipgloss.NewStyle().Faint(true)

	lines := []string{
		"",
		" " + titleStyle.Render("AGENTICS BROADCAST SYSTEM") + dimStyle.Render("  v0.1.0"),
		" " + dimStyle.Render("─────────────────────────────────────────"),
		"",
	}

	descs := []string{
		"Launch tmux + Claude",
		"Pick a previous Claude session",
		"Configure broadcast options",
		"Exit vibecast",
	}

	for i, item := range m.menuItems {
		if i == m.MenuIndex {
			lines = append(lines, "  "+styles.FKeyAction.Render("▸ "+item))
			lines = append(lines, "    "+dimStyle.Render(descs[i]))
		} else {
			lines = append(lines, "    "+item)
			lines = append(lines, "    "+dimStyle.Render(descs[i]))
		}
		lines = append(lines, "")
	}

	r := styles.RenderFKeyEntry
	lines = append(lines, " "+strings.Join([]string{r("↑↓", " Navigate"), r("⏎", " Select"), r("q", " Quit")}, "  "))

	return strings.Join(lines, "\n")
}

func (m InfoModel) viewStarting() string {
	titleStyle := lipgloss.NewStyle().Foreground(styles.AccentColor).Bold(true)
	dimStyle := lipgloss.NewStyle().Faint(true)
	lines := []string{
		"",
		" " + titleStyle.Render("AGENTICS BROADCAST SYSTEM"),
		" " + dimStyle.Render("─────────────────────────────────────────"),
		"",
		"  Starting broadcast...",
		"",
		"  [✓] Creating tmux session",
		"  [✓] Starting Claude Code",
		"  [ ] Connecting to server...",
		"",
		"  > Please wait...",
	}
	return strings.Join(lines, "\n")
}

func (m InfoModel) viewLive() string {
	titleStyle := lipgloss.NewStyle().Foreground(styles.AccentColor).Bold(true)
	dimStyle := lipgloss.NewStyle().Faint(true)
	liveStyle := lipgloss.NewStyle().Foreground(styles.SuccessColor).Bold(true)

	lines := []string{
		"",
		" " + liveStyle.Render("● LIVE") + "  " + titleStyle.Render("AGENTICS BROADCAST SYSTEM"),
		" " + dimStyle.Render("─────────────────────────────────────────"),
		"",
		fmt.Sprintf("  Session: %-12s  Viewers: %d", m.SessionID, m.Viewers),
		fmt.Sprintf("  Uptime:  %s", m.Uptime),
		"",
	}

	// PIN code — large block letters with label
	if m.PinCode != "" {
		lines = append(lines, "  "+titleStyle.Render("JOIN CODE:"))
		lines = append(lines, "")
		bigLines := renderBigPIN(m.PinCode)
		for _, l := range bigLines {
			lines = append(lines, "  "+l)
		}
		// Small text PIN below for easy reading / copy
		spaced := ""
		for i, c := range m.PinCode {
			if i > 0 {
				spaced += "       "
			}
			spaced += fmt.Sprintf("  %c   ", c)
		}
		lines = append(lines, "  "+dimStyle.Render(spaced))
		lines = append(lines, "")
	}

	if m.URL != "" {
		lines = append(lines, "  "+titleStyle.Render("LINK:")+"  "+m.URL)
		lines = append(lines, "")
	}

	if len(m.Panes) > 0 {
		lines = append(lines, fmt.Sprintf("  Panes: %d", len(m.Panes)))
		for _, p := range m.Panes {
			marker := "  "
			if p.Active {
				marker = " ▸"
			}
			lines = append(lines, marker+" "+p.Name)
		}
		lines = append(lines, "")
	}

	return strings.Join(lines, "\n")
}

// bigDigits maps characters to 5-line tall block representations.
// Each character is rendered using █ and spaces, 6 chars wide.
var bigLetters = map[byte][5]string{
	'0': {"█████ ", "█   █ ", "█   █ ", "█   █ ", "█████ "},
	'1': {"  █   ", "  █   ", "  █   ", "  █   ", "  █   "},
	'2': {"█████ ", "    █ ", "█████ ", "█     ", "█████ "},
	'3': {"█████ ", "    █ ", "█████ ", "    █ ", "█████ "},
	'4': {"█   █ ", "█   █ ", "█████ ", "    █ ", "    █ "},
	'5': {"█████ ", "█     ", "█████ ", "    █ ", "█████ "},
	'6': {"█████ ", "█     ", "█████ ", "█   █ ", "█████ "},
	'7': {"█████ ", "    █ ", "    █ ", "    █ ", "    █ "},
	'8': {"█████ ", "█   █ ", "█████ ", "█   █ ", "█████ "},
	'9': {"█████ ", "█   █ ", "█████ ", "    █ ", "█████ "},
	'A': {"█████ ", "█   █ ", "█████ ", "█   █ ", "█   █ "},
	'B': {"████  ", "█   █ ", "████  ", "█   █ ", "████  "},
	'C': {"█████ ", "█     ", "█     ", "█     ", "█████ "},
	'D': {"████  ", "█   █ ", "█   █ ", "█   █ ", "████  "},
	'E': {"█████ ", "█     ", "████  ", "█     ", "█████ "},
	'F': {"█████ ", "█     ", "████  ", "█     ", "█     "},
	'G': {"█████ ", "█     ", "█ ███ ", "█   █ ", "█████ "},
	'H': {"█   █ ", "█   █ ", "█████ ", "█   █ ", "█   █ "},
	'I': {"█████ ", "  █   ", "  █   ", "  █   ", "█████ "},
	'J': {"█████ ", "    █ ", "    █ ", "█   █ ", "█████ "},
	'K': {"█   █ ", "█  █  ", "███   ", "█  █  ", "█   █ "},
	'L': {"█     ", "█     ", "█     ", "█     ", "█████ "},
	'M': {"█   █ ", "██ ██ ", "█ █ █ ", "█   █ ", "█   █ "},
	'N': {"█   █ ", "██  █ ", "█ █ █ ", "█  ██ ", "█   █ "},
	'O': {"█████ ", "█   █ ", "█   █ ", "█   █ ", "█████ "},
	'P': {"█████ ", "█   █ ", "█████ ", "█     ", "█     "},
	'Q': {"█████ ", "█   █ ", "█   █ ", "█  █  ", "████  "},
	'R': {"█████ ", "█   █ ", "████  ", "█  █  ", "█   █ "},
	'S': {"█████ ", "█     ", "█████ ", "    █ ", "█████ "},
	'T': {"█████ ", "  █   ", "  █   ", "  █   ", "  █   "},
	'U': {"█   █ ", "█   █ ", "█   █ ", "█   █ ", "█████ "},
	'V': {"█   █ ", "█   █ ", "█   █ ", " █ █  ", "  █   "},
	'W': {"█   █ ", "█   █ ", "█ █ █ ", "██ ██ ", "█   █ "},
	'X': {"█   █ ", " █ █  ", "  █   ", " █ █  ", "█   █ "},
	'Y': {"█   █ ", " █ █  ", "  █   ", "  █   ", "  █   "},
	'Z': {"█████ ", "   █  ", "  █   ", " █    ", "█████ "},
}

// renderBigPIN renders a PIN code as large block letters (5 lines tall).
func renderBigPIN(pin string) []string {
	rows := [5]strings.Builder{}
	for i := 0; i < len(pin); i++ {
		ch := pin[i]
		if ch >= 'a' && ch <= 'z' {
			ch -= 32 // uppercase
		}
		glyph, ok := bigLetters[ch]
		if !ok {
			glyph = [5]string{"      ", "      ", "  ?   ", "      ", "      "}
		}
		if i > 0 {
			for r := 0; r < 5; r++ {
				rows[r].WriteString("  ") // gap between letters
			}
		}
		for r := 0; r < 5; r++ {
			rows[r].WriteString(glyph[r])
		}
	}
	result := make([]string, 5)
	for r := 0; r < 5; r++ {
		result[r] = rows[r].String()
	}
	return result
}

func (m InfoModel) viewSettings() string {
	titleStyle := lipgloss.NewStyle().Foreground(styles.AccentColor).Bold(true)
	dimStyle := lipgloss.NewStyle().Faint(true)

	check := func(b bool) string {
		if b {
			return "x"
		}
		return " "
	}

	lines := []string{
		"",
		" " + titleStyle.Render("SETTINGS"),
		" " + dimStyle.Render("─────────────────────────────────────────"),
		"",
	}

	items := []struct{ label, desc string; checked bool }{
		{"Share prompts with viewers", "Show Claude prompts in the viewer overlay", m.PromptSharing},
		{"Share project info", "Send workspace/project name to viewers", m.ShareProjectInfo},
	}

	for i, it := range items {
		prefix := "   "
		if i == m.SettingsIndex {
			prefix = "  ▸"
		}
		lines = append(lines, prefix+" ["+check(it.checked)+"] "+it.label)
		lines = append(lines, "      "+dimStyle.Render(it.desc))
		lines = append(lines, "")
	}

	r := styles.RenderFKeyEntry
	lines = append(lines, " "+strings.Join([]string{r("↑↓", " Navigate"), r("⏎", " Toggle"), r("Esc", " Back")}, "  "))

	return strings.Join(lines, "\n")
}

func (m InfoModel) viewSessions() string {
	titleStyle := lipgloss.NewStyle().Foreground(styles.AccentColor).Bold(true)
	dimStyle := lipgloss.NewStyle().Faint(true)

	lines := []string{
		"",
		" " + titleStyle.Render("RESUME SESSION"),
		" " + dimStyle.Render("─────────────────────────────────────────"),
		"",
	}

	if len(m.Sessions) == 0 {
		lines = append(lines, "  No sessions found for this project.")
		lines = append(lines, "")
		lines = append(lines, "  "+dimStyle.Render("Sessions are stored in ~/.claude/projects/"))
	} else {
		end := m.SessionScroll + 6
		if end > len(m.Sessions) {
			end = len(m.Sessions)
		}
		for i := m.SessionScroll; i < end; i++ {
			s := m.Sessions[i]
			shortID := s.ID
			if len(shortID) > 8 {
				shortID = shortID[:8]
			}
			prefix := "   "
			if i == m.SessionIndex {
				prefix = "  ▸"
			}
			lines = append(lines, prefix+" "+shortID+"  "+s.Updated)
			prompt := s.Prompt
			if prompt == "" {
				prompt = "(no prompt)"
			}
			if len(prompt) > 50 {
				prompt = prompt[:50] + "..."
			}
			lines = append(lines, "      "+dimStyle.Render(prompt))
		}
	}

	lines = append(lines, "")
	r := styles.RenderFKeyEntry
	lines = append(lines, " "+strings.Join([]string{r("↑↓", " Navigate"), r("⏎", " Select"), r("Esc", " Back")}, "  "))

	return strings.Join(lines, "\n")
}

// ── Compact Bar Model (2-line bottom pane in workspace windows) ──────────────

// BarModel is the 2-line persistent F-key bar at the bottom of workspace windows.
type BarModel struct {
	Width  int
	Height int
	Panes  []types.PaneStatus

	client *Client
	VSCode bool
}

// NewBarModel creates a 2-line workspace fkeybar.
func NewBarModel(sessionID string) BarModel {
	return BarModel{
		client: NewClient(),
		VSCode: os.Getenv("TERM_PROGRAM") == "vscode",
	}
}

func (m BarModel) Init() tea.Cmd {
	return tea.Batch(m.pollPanes(), scheduleStatusTick())
}

func (m BarModel) pollPanes() tea.Cmd {
	client := m.client
	return func() tea.Msg {
		panes, _ := client.GetPanes()
		return statusUpdatedMsg{Panes: panes}
	}
}

func (m BarModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.Width = msg.Width
		m.Height = msg.Height
		return m, nil

	case statusTickMsg:
		return m, tea.Batch(m.pollPanes(), scheduleStatusTick())

	case statusUpdatedMsg:
		if msg.Panes != nil {
			m.Panes = msg.Panes
		}
		return m, nil

	case tea.KeyMsg:
		// Compact bar doesn't handle keys — F-keys go via tmux bindings
		return m, nil
	}
	return m, nil
}

func (m BarModel) View() string {
	if m.Width == 0 {
		return ""
	}
	var lines []string
	if len(m.Panes) > 1 {
		lines = append(lines, m.renderPaneTabs())
	}
	lines = append(lines, m.renderFKeyRow())
	return strings.Join(lines, "\n")
}

func (m BarModel) renderPaneTabs() string {
	var parts []string
	for _, pane := range m.Panes {
		name := pane.Name
		if len(name) > 12 {
			name = name[:12]
		}
		if pane.Active {
			parts = append(parts, styles.FKeyAction.Render("▸ "+name))
		} else {
			parts = append(parts, lipgloss.NewStyle().Faint(true).Render(name))
		}
	}
	return " " + strings.Join(parts, " │ ")
}

func (m BarModel) renderFKeyRow() string {
	r := styles.RenderFKeyEntry
	if m.VSCode {
		return " " + strings.Join([]string{
			r("^b1", " Info"),
			r("^b2", " New"),
			r("^b3", " ◀"),
			r("^b4", " ▶"),
			r("^b5", " Close"),
			r("^b6", " Restart"),
			r("^b9", " Help"),
			r("^b0", " Stop"),
		}, " ")
	}
	return " " + strings.Join([]string{
		r("F1", " Info"),
		r("F2", " New"),
		r("F3", " ◀"),
		r("F4", " ▶"),
		r("F5", " Close"),
		r("F6", " Restart"),
		r("F9", " Help"),
		r("F10", " Stop"),
	}, " ")
}

// ── Help Model (full-screen help window) ─────────────────────────────────────

// HelpModel shows all keybindings and tips as a static help screen.
type HelpModel struct {
	Width  int
	Height int
	VSCode bool
}

// NewHelpModel creates the help screen model.
func NewHelpModel() HelpModel {
	return HelpModel{
		VSCode: os.Getenv("TERM_PROGRAM") == "vscode",
	}
}

func (m HelpModel) Init() tea.Cmd {
	// Keepalive tick so the program doesn't exit
	return tea.Tick(30*time.Second, func(time.Time) tea.Msg { return statusTickMsg{} })
}

func (m HelpModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.Width = msg.Width
		m.Height = msg.Height
	case statusTickMsg:
		return m, tea.Tick(30*time.Second, func(time.Time) tea.Msg { return statusTickMsg{} })
	case tea.KeyMsg:
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m HelpModel) View() string {
	if m.Width == 0 {
		return ""
	}

	title := lipgloss.NewStyle().Foreground(styles.AccentColor).Bold(true)
	heading := lipgloss.NewStyle().Bold(true).Underline(true)
	key := lipgloss.NewStyle().Foreground(styles.AccentColor).Bold(true)
	dim := lipgloss.NewStyle().Faint(true)

	prefix := "F"
	if m.VSCode {
		prefix = "^b"
	}
	fk := func(num string) string {
		return key.Render(prefix + num)
	}

	lines := []string{
		"",
		" " + title.Render("AGENTIC LIVE") + "  " + dim.Render("Keyboard Reference"),
		" " + strings.Repeat("─", 50),
		"",
		" " + heading.Render("Broadcast Controls"),
		"",
		"   " + fk("1") + "  Toggle between Info and Workspace",
		"   " + fk("2") + "  New agent pane (spawn additional Claude)",
		"   " + fk("3") + "  Previous window / agent",
		"   " + fk("4") + "  Next window / agent",
		"   " + fk("5") + "  Close current agent pane",
		"   " + fk("6") + "  Restart Claude in current pane",
	}
	if m.VSCode {
		lines = append(lines, "   "+fk("9")+"  This help screen")
		lines = append(lines, "   "+fk("0")+"  Stop broadcasting")
	} else {
		lines = append(lines, "   "+fk("9")+"   This help screen")
		lines = append(lines, "   "+fk("10")+"  Stop broadcasting")
	}
	lines = append(lines,
		"",
		" "+heading.Render("tmux Essentials"),
		"",
		"   "+key.Render("^b d")+"      Detach from session (vibecast keeps running)",
		"   "+key.Render("^b z")+"      Zoom current pane (toggle fullscreen)",
		"   "+key.Render("^b [")+"      Scroll mode (use arrows/PgUp/PgDn, "+key.Render("q")+" to exit)",
		"   "+key.Render("^b c")+"      New tmux window",
		"   "+key.Render("^b ,")+"      Rename current window",
		"   "+key.Render("^b w")+"      Window picker",
		"   "+key.Render("^b t")+"      Show clock",
		"",
		" "+heading.Render("Claude Code"),
		"",
		"   "+key.Render("Enter")+"     Send message to Claude",
		"   "+key.Render("Esc")+"       Cancel current generation",
		"   "+key.Render("/help")+"     Show Claude Code help",
		"   "+key.Render("/clear")+"    Clear conversation history",
		"   "+key.Render("/compact")+"  Compact conversation (reduce context)",
		"   "+key.Render("/cost")+"     Show token usage and cost",
		"   "+key.Render("/doctor")+"   Check environment health",
		"   "+key.Render("/model")+"    Switch Claude model",
		"   "+key.Render("Shift+Tab")+" Cycle permission mode",
		"",
		" "+heading.Render("Tips"),
		"",
		"   "+dim.Render("•")+" Viewers see your terminal live — they see what you see",
		"   "+dim.Render("•")+" The Info screen shows your join code and stream link",
		"   "+dim.Render("•")+" Multiple agents: use "+fk("2")+" to spawn, "+fk("3")+"/"+fk("4")+" to switch",
		"   "+dim.Render("•")+" Set "+key.Render("VIBECAST_DEBUG=1")+" for verbose logging",
		"",
	)

	// F-key row at bottom
	r := styles.RenderFKeyEntry
	var fkeyRow string
	if m.VSCode {
		fkeyRow = " " + strings.Join([]string{
			r("^b1", " Info"),
			r("^b9", " Help"),
			r("^b0", " Stop"),
		}, "  ")
	} else {
		fkeyRow = " " + strings.Join([]string{
			r("F1", " Info"),
			r("F9", " Help"),
			r("F10", " Stop"),
		}, "  ")
	}

	content := strings.Join(lines, "\n")
	contentLines := strings.Count(content, "\n") + 1
	fkeyLines := strings.Count(fkeyRow, "\n") + 1
	padding := m.Height - contentLines - fkeyLines
	if padding < 0 {
		padding = 0
	}

	return content + strings.Repeat("\n", padding) + fkeyRow
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func scheduleStatusTick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg {
		return statusTickMsg{}
	})
}
