package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/pksorensen/vibecast/internal/session"
	"github.com/pksorensen/vibecast/internal/stream"
	"github.com/pksorensen/vibecast/internal/styles"
	"github.com/pksorensen/vibecast/internal/types"
	"github.com/pksorensen/vibecast/internal/util"
)

// Update handles all Bubble Tea messages.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.Phase == types.PhaseSettings {
			return m.updateSettings(msg)
		}

		if m.PhaseApprovals {
			switch msg.String() {
			case "esc":
				m.PhaseApprovals = false
				return m, nil
			case "up", "k":
				if m.ApprovalIndex > 0 {
					m.ApprovalIndex--
				}
				return m, nil
			case "down", "j":
				if m.ApprovalIndex < len(m.PendingImages)-1 {
					m.ApprovalIndex++
				}
				return m, nil
			case "enter":
				if m.ApprovalIndex < len(m.PendingImages) {
					img := m.PendingImages[m.ApprovalIndex]
					go stream.ApproveImage(m.SessionID, img.ImageID, true)
					m.PendingImages = append(m.PendingImages[:m.ApprovalIndex], m.PendingImages[m.ApprovalIndex+1:]...)
					m.ImageApprovals--
					if m.ApprovalIndex >= len(m.PendingImages) {
						m.ApprovalIndex = max(0, len(m.PendingImages)-1)
					}
					if len(m.PendingImages) == 0 {
						m.PhaseApprovals = false
					}
					if m.Status != nil {
						m.Status.Mu.Lock()
						m.Status.PendingImages = m.ImageApprovals
						m.Status.Mu.Unlock()
					}
				}
				return m, nil
			case "x":
				if m.ApprovalIndex < len(m.PendingImages) {
					img := m.PendingImages[m.ApprovalIndex]
					go stream.ApproveImage(m.SessionID, img.ImageID, false)
					m.PendingImages = append(m.PendingImages[:m.ApprovalIndex], m.PendingImages[m.ApprovalIndex+1:]...)
					m.ImageApprovals--
					if m.ApprovalIndex >= len(m.PendingImages) {
						m.ApprovalIndex = max(0, len(m.PendingImages)-1)
					}
					if len(m.PendingImages) == 0 {
						m.PhaseApprovals = false
					}
					if m.Status != nil {
						m.Status.Mu.Lock()
						m.Status.PendingImages = m.ImageApprovals
						m.Status.Mu.Unlock()
					}
				}
				return m, nil
			}
		}

		key := msg.String()

		switch key {
		case "ctrl+c", "q":
			if m.Phase == types.PhaseLive {
				m.Phase = types.PhaseStopping
				m.TransFrame = 0
				m.TransDone = false
				return m, tea.Batch(stream.StopStream(m.TtydPID, m.TmuxSession, m.SessionID, m.PromptSharing, m.Panes, m.ResumeMode), TransTick())
			}
			return m, tea.Quit
		case "enter", " ":
			if m.Phase == types.PhaseSplash && m.SplashDone {
				if m.ResumeMode && m.ResumeSessionID != "" {
					m.Phase = types.PhaseStarting
					m.TransFrame = 0
					m.TransDone = false
					return m, tea.Batch(m.Spinner.Tick, stream.ResumeStream(m.ResumeSessionID, m.Status), TransTick())
				}
				m.Phase = types.PhaseMenu
				m.TransFrame = 0
				m.TransDone = false
				return m, TransTick()
			}
			if m.Phase == types.PhaseMenu {
				switch m.MenuIndex {
				case 0:
					m.Phase = types.PhaseStarting
					m.TransFrame = 0
					m.TransDone = false
					return m, tea.Batch(m.Spinner.Tick, stream.StartStream(m.PromptSharing, m.ShareProjectInfo, m.ProjectName, m.ResumeSessionID, m.BroadcastID, "", m.Status), TransTick())
				case 1:
					m.ClaudeSessions = session.ScanClaudeSessions()
					m.SessionIndex = 0
					m.SessionScroll = 0
					m.Phase = types.PhaseSessions
					m.TransFrame = 0
					m.TransDone = false
					return m, TransTick()
				case 2:
					m.Phase = types.PhaseSettings
					m.SettingsIndex = 0
					m.TransFrame = 0
					m.TransDone = false
					return m, TransTick()
				case 3:
					return m, tea.Quit
				}
			}
			if m.Phase == types.PhaseSessions {
				if len(m.ClaudeSessions) > 0 {
					selected := m.ClaudeSessions[m.SessionIndex]
					if m.SessionID != "" {
						m.ClaudeSessionID = selected.SessionID
						m.Phase = types.PhaseLive
						return m, stream.RestartClaude(m.TmuxSession, true, selected.SessionID)
					}
					m.Phase = types.PhaseStarting
					m.TransFrame = 0
					m.TransDone = false
					return m, tea.Batch(m.Spinner.Tick, stream.StartStream(m.PromptSharing, m.ShareProjectInfo, m.ProjectName, m.ResumeSessionID, m.BroadcastID, selected.SessionID, m.Status), TransTick())
				}
			}
		case "up", "k":
			if m.Phase == types.PhaseMenu && m.MenuIndex > 0 {
				m.MenuIndex--
			}
			if m.Phase == types.PhaseSessions && m.SessionIndex > 0 {
				m.SessionIndex--
				if m.SessionIndex < m.SessionScroll {
					m.SessionScroll = m.SessionIndex
				}
			}
		case "down", "j":
			if m.Phase == types.PhaseMenu && m.MenuIndex < 3 {
				m.MenuIndex++
			}
			if m.Phase == types.PhaseSessions && m.SessionIndex < len(m.ClaudeSessions)-1 {
				m.SessionIndex++
				maxVisible := 6
				if m.SessionIndex >= m.SessionScroll+maxVisible {
					m.SessionScroll = m.SessionIndex - maxVisible + 1
				}
			}
		case "esc":
			if m.Phase == types.PhaseSessions {
				if m.SessionID != "" {
					m.Phase = types.PhaseLive
				} else {
					m.Phase = types.PhaseMenu
				}
				m.TransFrame = 0
				m.TransDone = false
				return m, TransTick()
			}
		case "s":
			if m.Phase == types.PhaseLive {
				m.Phase = types.PhaseStopping
				m.TransFrame = 0
				m.TransDone = false
				return m, tea.Batch(m.Spinner.Tick, stream.StopStream(m.TtydPID, m.TmuxSession, m.SessionID, m.PromptSharing, m.Panes, m.ResumeMode), TransTick())
			}
		case "w":
			if m.Phase == types.PhaseLive {
				targetWindow := "main"
				if len(m.Panes) > 0 && m.ActivePaneIdx < len(m.Panes) {
					targetWindow = m.Panes[m.ActivePaneIdx].PaneId
				}
				c := exec.Command("tmux", "attach", "-t", m.TmuxSession+":"+targetWindow)
				c.Env = append(util.FilterEnv(os.Environ(), "TMUX"), "TERM="+os.Getenv("TERM"))
				return m, tea.ExecProcess(c, func(err error) tea.Msg {
					return types.TmuxDetachedMsg{Err: err}
				})
			}
		case "i":
			if m.Phase == types.PhaseLive && len(m.PendingImages) > 0 {
				m.PhaseApprovals = true
				m.ApprovalIndex = 0
			}
		}

	case tea.WindowSizeMsg:
		m.Width = msg.Width
		m.Height = msg.Height

	case types.TransTickMsg:
		if m.TransFrame < styles.TransMaxFrames {
			m.TransFrame++
			return m, TransTick()
		}
		m.TransDone = true
		return m, nil

	case types.SplashTickMsg:
		if m.SplashFrame < styles.SplashMaxFrames {
			m.SplashFrame++
			return m, SplashTick()
		}
		m.SplashDone = true
		return m, nil

	case types.TmuxDetachedMsg:
		return m, UptimeTick()

	case types.ClaudeRestartedMsg:
		return m, nil

	case types.ControlRestartMsg:
		if m.Phase == types.PhaseLive {
			return m, stream.RestartClaude(m.TmuxSession, true, m.ClaudeSessionID)
		}
		return m, nil

	case types.ControlStopMsg:
		if m.Phase == types.PhaseLive {
			m.Phase = types.PhaseStopping
			m.Status.Phase = "stopping" // tell the stop hook stop_broadcast was already called
			m.TransFrame = 0
			m.TransDone = false
			return m, tea.Batch(m.Spinner.Tick, stream.StopStream(m.TtydPID, m.TmuxSession, m.SessionID, m.PromptSharing, m.Panes, m.ResumeMode, msg.Message, msg.Conclusion, msg.GitCommit, msg.GitBranch, msg.GitPushError), TransTick())
		}
		// If waiting/menu, just quit
		return m, tea.Quit

	case types.ControlServerChangedMsg:
		m.StreamURL = msg.URL
		return m, nil

	case types.PaneSpawnedMsg:
		m.Panes = append(m.Panes, msg.Pane)
		if sf, err := session.ReadSessionFile(filepath.Join(session.SessionsDir(), m.SessionID+".json")); err == nil {
			sf.Panes = append(sf.Panes, types.SessionFilePaneEntry{
				PaneID:          msg.Pane.PaneId,
				ClaudeSessionID: msg.Pane.ClaudeSessionID,
			})
			session.WriteSessionFile(*sf)
		}
		return m, nil

	case types.PaneClosedMsg:
		if msg.Err != nil {
			fmt.Fprintf(os.Stderr, "[pane] close error for %s: %v\n", msg.PaneId, msg.Err)
		}
		for i, p := range m.Panes {
			if p.PaneId == msg.PaneId {
				m.Panes = append(m.Panes[:i], m.Panes[i+1:]...)
				break
			}
		}
		if m.ActivePaneIdx >= len(m.Panes) {
			m.ActivePaneIdx = max(0, len(m.Panes)-1)
		}
		if sf, err := session.ReadSessionFile(filepath.Join(session.SessionsDir(), m.SessionID+".json")); err == nil {
			var newPanes []types.SessionFilePaneEntry
			for _, pe := range sf.Panes {
				if pe.PaneID != msg.PaneId {
					newPanes = append(newPanes, pe)
				}
			}
			sf.Panes = newPanes
			session.WriteSessionFile(*sf)
		}
		return m, nil

	case types.ImageQueuedMsg:
		if m.AutoApproveImages {
			go stream.ApproveImage(m.SessionID, msg.Img.ImageID, true)
			return m, nil
		}
		m.PendingImages = append(m.PendingImages, msg.Img)
		m.ImageApprovals++
		if m.Status != nil {
			m.Status.Mu.Lock()
			m.Status.PendingImages = m.ImageApprovals
			m.Status.Mu.Unlock()
		}
		if m.TmuxSession != "" {
			exec.Command("tmux", "set-option", "-t", m.TmuxSession, "status-right",
				fmt.Sprintf(" +%d imgs | LIVE ", m.ImageApprovals)).Run()
		}
		return m, nil

	case types.StreamStartedMsg:
		m.Phase = types.PhaseLive
		m.SessionID = msg.SessionID
		m.BroadcastID = msg.BroadcastID
		m.StreamURL = msg.URL
		m.PinCode = msg.PinCode
		m.TtydPID = msg.PID
		m.TtydPort = msg.TtydPort
		m.TmuxSession = "vibecast-" + msg.SessionID
		exec.Command("tmux", "set-option", "-t", m.TmuxSession, "status-left",
			fmt.Sprintf(" 🔴 AGENTIC LIVE  /live/%s ", msg.BroadcastID)).Run()
		m.MetaCh = msg.MetaCh
		m.ClaudeSessionID = msg.ClaudeSessionID
		m.StartTime = time.Now()
		m.TransFrame = 0
		m.TransDone = false
		if msg.MainPane != nil {
			m.Panes = []types.PaneInfo{*msg.MainPane}
			m.ActivePaneIdx = 0
		}
		if m.Status != nil {
			m.Status.Mu.Lock()
			m.Status.SessionID = msg.SessionID
			m.Status.BroadcastID = msg.BroadcastID
			m.Status.URL = msg.URL
			m.Status.Phase = "live"
			m.Status.ClaudeSessionID = msg.ClaudeSessionID
			m.Status.TmuxSession = m.TmuxSession
			m.Status.Mu.Unlock()
		}
		// The lobby fkeybar will detect the phase change via polling and
		// call switch-client itself (since it runs inside the tmux client).
		// Give it a moment to switch, then kill the lobby session.
		go func() {
			time.Sleep(3 * time.Second)
			exec.Command("tmux", "kill-session", "-t", "vibecast-lobby").Run()
		}()
		// Broadcast phase change to fkeybar instances
		if m.Status != nil {
			m.Status.BroadcastEvent(fmt.Sprintf(`{"type":"phase","phase":"live","sessionId":"%s"}`, msg.SessionID))
		}
		return m, tea.Batch(UptimeTick(), TransTick())

	case types.StreamErrorMsg:
		m.Err = msg.Err
		if m.Phase == types.PhaseWaiting || m.Phase == types.PhaseStarting {
			// Broadcast error to fkeybar, stay in waiting
			m.Phase = types.PhaseWaiting
			if m.Status != nil {
				m.Status.Mu.Lock()
				m.Status.Phase = "menu"
				m.Status.Mu.Unlock()
				m.Status.BroadcastEvent(fmt.Sprintf(`{"type":"error","message":"%s"}`, msg.Err.Error()))
			}
			return m, nil
		}
		m.Phase = types.PhaseMenu
		m.TransFrame = 0
		m.TransDone = false
		return m, TransTick()

	case types.StreamStoppedMsg:
		return m, tea.Quit

	case types.UptimeTickMsg:
		if m.Phase == types.PhaseLive {
			m.Uptime = time.Since(m.StartTime).Round(time.Second)
			if m.Status != nil {
				m.Status.Mu.Lock()
				m.Status.Uptime = m.Uptime.String()
				m.Status.Mu.Unlock()
			}
			return m, UptimeTick()
		}

	case types.ChatMsgReceived:
		if msg.Msg.Type == "viewers" {
			m.ViewerCount = msg.Msg.Count
			if m.Status != nil {
				m.Status.Mu.Lock()
				m.Status.Viewers = msg.Msg.Count
				m.Status.Mu.Unlock()
			}
		} else {
			m.ChatMessages = append(m.ChatMessages, msg.Msg)
			if len(m.ChatMessages) > 50 {
				m.ChatMessages = m.ChatMessages[len(m.ChatMessages)-50:]
			}
		}
		return m, nil

	case types.StartStreamRequestMsg:
		m.PromptSharing = msg.PromptSharing
		m.ShareProjectInfo = msg.ShareProjectInfo
		m.Phase = types.PhaseStarting
		m.TransFrame = 0
		m.TransDone = false
		if m.Status != nil {
			m.Status.Mu.Lock()
			m.Status.Phase = "starting"
			m.Status.Mu.Unlock()
			m.Status.BroadcastEvent(`{"type":"phase","phase":"starting"}`)
		}
		return m, tea.Batch(m.Spinner.Tick, stream.StartStream(msg.PromptSharing, msg.ShareProjectInfo, m.ProjectName, m.ResumeSessionID, m.BroadcastID, m.ClaudeResumeID, m.Status), TransTick())

	case types.FKeyActionMsg:
		// F-key actions forwarded from control socket
		// F1=Info (handled by control.go), F2=New, F3=Prev, F4=Next, F5=Close, F6=Restart, F9=Session, F10=Stop
		if m.Phase == types.PhaseLive {
			switch msg.Key {
			case "f2": // New pane
				sessionName := m.TmuxSession
				sessionID := m.SessionID
				paneIdx := len(m.Panes) + 1
				paneId := fmt.Sprintf("pane%d", paneIdx)
				paneName := fmt.Sprintf("Pane %d", paneIdx)
				return m, func() tea.Msg {
					pane, err := stream.SpawnPane(sessionName, sessionID, paneId, paneName, m.Status, "")
					if err != nil {
						return types.PaneClosedMsg{PaneId: paneId, Err: err}
					}
					return types.PaneSpawnedMsg{Pane: *pane}
				}
			case "f3": // Prev pane
				if m.ActivePaneIdx > 0 {
					m.ActivePaneIdx--
					stream.NotifyActivePaneChange(m.Panes, m.ActivePaneIdx)
				}
			case "f4": // Next pane
				if m.ActivePaneIdx < len(m.Panes)-1 {
					m.ActivePaneIdx++
					stream.NotifyActivePaneChange(m.Panes, m.ActivePaneIdx)
				}
			case "f5": // Close pane
				if len(m.Panes) > 1 {
					pane := m.Panes[m.ActivePaneIdx]
					paneId := pane.PaneId
					ttydPID := pane.TtydPID
					tmuxSession := m.TmuxSession
					return m, func() tea.Msg {
						if ttydPID > 0 {
							if proc, err := os.FindProcess(ttydPID); err == nil {
								proc.Kill()
							}
						}
						groupSess := tmuxSession + "-ttyd-" + paneId
						exec.Command("tmux", "kill-session", "-t", groupSess).Run()
						exec.Command("tmux", "kill-window", "-t", tmuxSession+":"+paneId).Run()
						return types.PaneClosedMsg{PaneId: paneId}
					}
				}
			case "f7": // Toggle chat
				m.ShowChat = !m.ShowChat
			case "f8": // Image approvals
				if len(m.PendingImages) > 0 {
					m.PhaseApprovals = true
					m.ApprovalIndex = 0
				}
			}
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.Spinner, cmd = m.Spinner.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m Model) updateSettings(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		m.Phase = types.PhaseMenu
		m.TransFrame = 0
		m.TransDone = false
		return m, TransTick()
	case "up", "k":
		if m.SettingsIndex > 0 {
			m.SettingsIndex--
		}
	case "down", "j":
		if m.SettingsIndex < 1 {
			m.SettingsIndex++
		}
	case "enter", " ":
		switch m.SettingsIndex {
		case 0:
			m.PromptSharing = !m.PromptSharing
		case 1:
			m.ShareProjectInfo = !m.ShareProjectInfo
		}
	}
	return m, nil
}
