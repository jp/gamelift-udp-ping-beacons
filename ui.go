package main

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type scanCompleteMsg report

type progressClosedMsg struct{}

type scanTickMsg time.Time

type tuiModel struct {
	ctx       context.Context
	cancel    context.CancelFunc
	endpoints []endpoint
	duration  time.Duration
	interval  time.Duration
	timeout   time.Duration
	family    string

	progressEvents chan progressEvent
	table          table.Model
	report         *report
	liveResults    []result
	completedRows  []bool
	scanPause      *pauseState

	width     int
	height    int
	completed int
	started   time.Time
	elapsed   time.Duration
	paused    bool
	done      bool
}

var (
	uiTitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#00FFFF"))

	uiMutedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#8A8A8A"))

	uiGoodStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#00D787"))

	uiWarnStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FFAF00"))

	uiBadStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FF5F87"))
)

func runTUI(parent context.Context, endpoints []endpoint, duration, interval, timeout time.Duration, family string) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	liveResults := initialResults(endpoints, family)
	tbl := table.New(
		table.WithColumns(resultColumns(120)),
		table.WithRows(resultRows(liveResults)),
		table.WithFocused(true),
		table.WithHeight(12),
	)
	tbl.SetStyles(tableStyles())

	model := tuiModel{
		ctx:            ctx,
		cancel:         cancel,
		endpoints:      endpoints,
		duration:       duration,
		interval:       interval,
		timeout:        timeout,
		family:         family,
		progressEvents: make(chan progressEvent, len(endpoints)*64),
		table:          tbl,
		liveResults:    liveResults,
		completedRows:  make([]bool, len(endpoints)),
		scanPause:      &pauseState{},
		started:        time.Now(),
	}

	program := tea.NewProgram(model, tea.WithAltScreen())
	_, err := program.Run()
	return err
}

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(m.scanTickCmd(), m.scanCmd(), m.waitProgressCmd())
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc", "q":
			m.cancel()
			return m, tea.Quit
		case " ":
			if !m.done {
				m.paused = m.scanPause.toggle()
			}
			return m, nil
		case "enter":
			if m.done {
				return m, tea.Quit
			}
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.table.SetColumns(resultColumns(msg.Width))
		m.table.SetHeight(clamp(msg.Height-15, 8, 28))

	case scanTickMsg:
		if !m.started.IsZero() {
			m.elapsed = time.Since(m.started)
		}
		if m.done {
			return m, nil
		}
		return m, m.scanTickCmd()

	case progressEvent:
		if m.done {
			return m, nil
		}
		if msg.Index >= 0 && msg.Index < len(m.liveResults) {
			m.liveResults[msg.Index] = msg.Result
			if msg.Done && !m.completedRows[msg.Index] {
				m.completedRows[msg.Index] = true
				m.completed++
			}
			m.table.SetRows(resultRows(m.liveResults))
		}
		return m, m.waitProgressCmd()

	case progressClosedMsg:
		m.completed = len(m.endpoints)
		return m, nil

	case scanCompleteMsg:
		rep := report(msg)
		m.report = &rep
		m.done = true
		m.completed = len(m.endpoints)
		m.elapsed = m.duration
		m.liveResults = rep.Results
		m.table.SetRows(resultRows(m.liveResults))
		return m, nil
	}

	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

func (m tuiModel) View() string {
	var b strings.Builder
	b.WriteString(uiTitleStyle.Render("GameLift UDP Ping Beacon Report"))
	b.WriteString("\n")
	status := "running"
	if m.paused {
		status = "paused"
	}
	b.WriteString(uiMutedStyle.Render(fmt.Sprintf(
		"%d endpoints | %s | interval %s | timeout %s | family %s | %s | space pause/resume | q quit",
		len(m.endpoints), durationLabel(m.duration), m.interval, m.timeout, m.family, status,
	)))
	b.WriteString("\n\n")

	if !m.done {
		b.WriteString(m.table.View())
	} else {
		b.WriteString(m.resultView())
	}

	b.WriteString("\n")
	return b.String()
}

func (m tuiModel) resultView() string {
	if m.report == nil {
		return uiBadStyle.Render("No report was generated.")
	}

	summary := summarizeResults(m.report.Results)
	lines := []string{
		fmt.Sprintf("%s %d healthy  %s %d degraded  %s %d unavailable",
			uiGoodStyle.Render("ok"), summary.ok,
			uiWarnStyle.Render("loss"), summary.degraded,
			uiBadStyle.Render("down"), summary.down,
		),
		uiMutedStyle.Render("Use arrow keys to scroll. Press Enter or q to quit."),
		"",
		m.table.View(),
	}
	return strings.Join(lines, "\n")
}

func (m tuiModel) scanCmd() tea.Cmd {
	return func() tea.Msg {
		results := runAll(m.ctx, m.endpoints, m.duration, m.interval, m.timeout, m.family, m.progressEvents, m.scanPause)
		close(m.progressEvents)
		return scanCompleteMsg(report{
			GeneratedAt: time.Now().UTC(),
			Duration:    m.duration.String(),
			Interval:    m.interval.String(),
			Timeout:     m.timeout.String(),
			Family:      m.family,
			OS:          runtime.GOOS,
			Arch:        runtime.GOARCH,
			Results:     results,
		})
	}
}

func durationLabel(duration time.Duration) string {
	if duration == 0 {
		return "continuous"
	}
	return "duration " + duration.String()
}

func initialResults(endpoints []endpoint, family string) []result {
	results := make([]result, len(endpoints))
	for i, ep := range endpoints {
		results[i] = result{
			Endpoint: ep,
			Network:  networkFor(ep, family),
		}
	}
	return results
}

func (m tuiModel) waitProgressCmd() tea.Cmd {
	return func() tea.Msg {
		event, ok := <-m.progressEvents
		if !ok {
			return progressClosedMsg{}
		}
		return event
	}
}

func (m tuiModel) scanTickCmd() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
		return scanTickMsg(t)
	})
}

type resultSummary struct {
	ok       int
	degraded int
	down     int
}

func summarizeResults(results []result) resultSummary {
	var summary resultSummary
	for _, res := range results {
		switch {
		case res.Error != "" || res.Received == 0:
			summary.down++
		case res.Loss > 0:
			summary.degraded++
		default:
			summary.ok++
		}
	}
	return summary
}

func resultColumns(width int) []table.Column {
	addressWidth := clamp(width-108, 22, 40)
	locationWidth := clamp(width-126, 16, 24)
	return []table.Column{
		{Title: "Code", Width: 16},
		{Title: "Location", Width: locationWidth},
		{Title: "Address", Width: addressWidth},
		{Title: "Sent", Width: 5},
		{Title: "Recv", Width: 5},
		{Title: "Loss", Width: 7},
		{Title: "Avg", Width: 8},
		{Title: "P50", Width: 8},
		{Title: "P95", Width: 8},
		{Title: "Jitter", Width: 8},
		{Title: "Status", Width: 12},
	}
}

func resultRows(results []result) []table.Row {
	rows := make([]table.Row, 0, len(results))
	for _, res := range results {
		rows = append(rows, table.Row{
			res.Endpoint.Code,
			res.Endpoint.Name,
			hostOnly(res.Endpoint.Address),
			fmt.Sprintf("%d", res.Sent),
			fmt.Sprintf("%d", res.Received),
			fmt.Sprintf("%.1f%%", res.Loss),
			formatDuration(res.Avg),
			formatDuration(res.P50),
			formatDuration(res.P95),
			formatDuration(res.Jitter),
			resultStatus(res),
		})
	}
	return rows
}

func resultStatus(res result) string {
	if res.Error != "" {
		return "error"
	}
	if res.Sent == 0 {
		return "waiting"
	}
	if res.Received == 0 {
		return "no replies"
	}
	if res.Loss > 0 {
		return "packet loss"
	}
	return "ok"
}

func tableStyles() table.Styles {
	styles := table.DefaultStyles()
	styles.Header = styles.Header.
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color("#3A3A3A")).
		BorderBottom(true).
		Bold(true).
		Foreground(lipgloss.Color("#00FFFF"))
	styles.Selected = styles.Selected.
		Foreground(lipgloss.Color("#0A0A0A")).
		Background(lipgloss.Color("#00FFFF")).
		Bold(false)
	return styles
}

func clamp(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}
