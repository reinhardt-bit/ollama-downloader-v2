package ui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"ollama-downloader-v2/client"
)

const (
	padding    = 2
	maxWidth   = 80
	listHeight = 14
)

var (
	titleStyle        = lipgloss.NewStyle().MarginLeft(2)
	itemStyle         = lipgloss.NewStyle().PaddingLeft(4)
	selectedItemStyle = lipgloss.NewStyle().PaddingLeft(2).Foreground(lipgloss.Color("170"))
	paginationStyle   = list.DefaultStyles().PaginationStyle.PaddingLeft(4)
	helpStyle         = list.DefaultStyles().HelpStyle.PaddingLeft(4).PaddingBottom(1)
	quitTextStyle     = lipgloss.NewStyle().Margin(1, 0, 2, 4)
	detailsStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).MarginLeft(2)
)

type item string

func (i item) FilterValue() string { return "" }

type itemDelegate struct{}

func (d itemDelegate) Height() int                             { return 1 }
func (d itemDelegate) Spacing() int                            { return 0 }
func (d itemDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }
func (d itemDelegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	i, ok := listItem.(item)
	if !ok {
		return
	}
	str := fmt.Sprintf("%d. %s", index+1, i)
	fn := itemStyle.Render
	if index == m.Index() {
		fn = func(s ...string) string {
			return selectedItemStyle.Render("> " + strings.Join(s, " "))
		}
	}
	fmt.Fprint(w, fn(str))
}

type Model struct {
	progress    progress.Model
	percent     float64
	status      string
	modelToPull string
	host        string
	cancel      context.CancelFunc

	list           list.Model
	quitting       bool
	selectedChoice string
	showList       bool
	quitUICh       chan struct{}
	userChoiceCh   chan string

	// --- CORRECTED FIELDS for speed/ETA calculation ---
	// Total size of the download
	totalBytes int64
	// The most up-to-date record of bytes downloaded
	lastCompletedBytes int64
	// A snapshot of bytes downloaded at the last 1-second tick
	bytesAtLastTick int64
	// Calculated speed in bytes per second
	speed float64
}

func NewModel(modelToPull string, host string, cancel context.CancelFunc, quitUICh chan struct{}, userChoiceCh chan string) Model {
	items := []list.Item{
		item("Continue (until next error)"),
		item("Continue (until download completed)"),
		item("Quit"),
	}

	l := list.New(items, itemDelegate{}, maxWidth, listHeight)
	l.Title = "Connection timed out or context deadline exceeded. Choose an option:"
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(false)
	l.Styles.Title = titleStyle
	l.Styles.PaginationStyle = paginationStyle
	l.Styles.HelpStyle = helpStyle

	return Model{
		progress:     progress.New(progress.WithDefaultGradient()),
		status:       "Connecting to Ollama...",
		modelToPull:  modelToPull,
		host:         host,
		cancel:       cancel,
		list:         l,
		showList:     false,
		quitUICh:     quitUICh,
		userChoiceCh: userChoiceCh,
	}
}

// A ticker is used to create a stable 1-second interval for speed calculation.
func (m Model) Init() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return t })
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.progress.Width = msg.Width - padding*2 - 4
		if m.progress.Width > maxWidth {
			m.progress.Width = maxWidth
		}
		m.list.SetWidth(msg.Width)
		return m, nil

	case tea.KeyMsg:
		switch keypress := msg.String(); keypress {
		case "q", "ctrl+c":
			m.quitting = true
			m.selectedChoice = "Quit"
			close(m.quitUICh)
			m.userChoiceCh <- m.selectedChoice
			return m, tea.Quit

		case "enter":
			if m.showList {
				i, ok := m.list.SelectedItem().(item)
				if ok {
					m.selectedChoice = string(i)
				}
				close(m.quitUICh)
				m.userChoiceCh <- m.selectedChoice
				return m, tea.Quit
			}
		}
		var cmd tea.Cmd
		if m.showList {
			m.list, cmd = m.list.Update(msg)
		}
		return m, cmd

	case client.ProgressMsg:
		// This message now ONLY updates the state. Speed calculation is moved.
		m.status = msg.Status
		if msg.Total > 0 {
			m.totalBytes = msg.Total
			m.percent = float64(msg.Completed) / float64(msg.Total)
		} else {
			m.percent = 0
		}
		m.lastCompletedBytes = msg.Completed
		return m, nil

	case client.TimeoutMsg:
		m.showList = true
		return m, nil

	case client.ErrorMsg:
		m.status = fmt.Sprintf("Error: %s", msg.Err)
		m.selectedChoice = "Quit"
		close(m.quitUICh)
		m.userChoiceCh <- "Quit"
		return m, tea.Quit

	case progress.FrameMsg:
		progressModel, cmd := m.progress.Update(msg)
		m.progress = progressModel.(progress.Model)
		return m, cmd

	// --- CORRECTED LOGIC ---
	// This is the 1-second tick message.
	case time.Time:
		// Calculate how many bytes were downloaded in the last second.
		bytesInLastSecond := m.lastCompletedBytes - m.bytesAtLastTick
		m.speed = float64(bytesInLastSecond) // Since the interval is 1s, this is bytes/sec.

		// Update the snapshot for the next tick's calculation.
		m.bytesAtLastTick = m.lastCompletedBytes

		// If the download is finished, stop calculating speed.
		if m.percent >= 1.0 {
			m.speed = 0
		}

		// Re-issue the tick command to continue the 1-second loop.
		return m, tea.Tick(time.Second, func(t time.Time) tea.Msg { return t })

	default:
		return m, nil
	}
}

// formatBytes is a helper to display byte counts in a human-readable way.
func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func (m Model) View() string {
	pad := lipgloss.NewStyle().Padding(1, 2)

	if m.showList {
		return "\n" + m.list.View()
	}

	var details string
	if m.totalBytes > 0 {
		speedStr := fmt.Sprintf("%.1f MB/s", m.speed/1024/1024)
		if m.speed < 1024*1024 {
			speedStr = fmt.Sprintf("%.1f KB/s", m.speed/1024)
		}

		downloadedStr := fmt.Sprintf("%s / %s", formatBytes(m.lastCompletedBytes), formatBytes(m.totalBytes))

		etaStr := "--"
		if m.speed > 0 && m.totalBytes > m.lastCompletedBytes {
			remainingBytes := m.totalBytes - m.lastCompletedBytes
			etaSeconds := float64(remainingBytes) / m.speed
			eta := time.Duration(etaSeconds) * time.Second
			etaStr = fmt.Sprintf("%s left", eta.Round(time.Second))
		}

		if m.percent >= 1.0 {
			speedStr = ""
			etaStr = "Complete"
		}

		details = detailsStyle.Render(lipgloss.JoinHorizontal(
			lipgloss.Top,
			downloadedStr,
			"  •  ",
			speedStr,
			"  •  ",
			etaStr,
		))
	}

	if m.percent == 0 && m.totalBytes == 0 {
		return pad.Render(m.status)
	}

	return pad.Render(fmt.Sprintf("%s\n%s\n%s", m.status, m.progress.ViewAs(m.percent), details))
}

func (m Model) GetSelectedChoice() string {
	return m.selectedChoice
}
