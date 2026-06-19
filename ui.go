package main

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type scanCompleteMsg report

type progressClosedMsg struct{}

type scanTickMsg time.Time

type traceCompleteMsg struct {
	Code   string
	Output string
	Err    error
}

type traceStoppedMsg struct{}

type viewMode int

const (
	tableMode viewMode = iota
	traceMode
)

const defaultTableHeight = 20

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
	viewport       viewport.Model
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
	mode      viewMode

	traceTarget  result
	traceRunning bool
	traceOutput  string
	traceError   error
	traceEvents  chan traceCompleteMsg
	traceCancel  context.CancelFunc
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
		table.WithRows(resultRows(sortedResultsByAvgLatency(liveResults))),
		table.WithFocused(true),
		table.WithHeight(defaultTableHeight),
	)
	tbl.SetStyles(tableStyles())
	vp := viewport.New(120, 20)

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
		viewport:       vp,
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
			if msg.String() == "esc" && m.mode == traceMode {
				m.stopTrace()
				m.mode = tableMode
				return m, nil
			}
			m.cancel()
			return m, tea.Quit
		case "backspace":
			if m.mode == traceMode {
				m.stopTrace()
				m.mode = tableMode
				return m, nil
			}
		case " ":
			if !m.done && m.mode == tableMode {
				m.paused = m.scanPause.toggle()
			}
			return m, nil
		case "enter":
			if m.mode == traceMode {
				return m, nil
			}
			target, ok := m.selectedResult()
			if !ok {
				return m, nil
			}
			m.mode = traceMode
			m.traceTarget = target
			m.traceRunning = true
			m.traceOutput = ""
			m.traceError = nil
			m.traceEvents = make(chan traceCompleteMsg, 32)
			traceCtx, traceCancel := context.WithCancel(m.ctx)
			m.traceCancel = traceCancel
			m.viewport.GotoTop()
			m.viewport.SetContent("Running custom traceroute...")
			return m, tea.Batch(startCustomTraceCmd(traceCtx, target, m.interval, m.timeout, m.traceEvents), m.waitTraceCmd())
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.table.SetColumns(resultColumns(msg.Width))
		m.table.SetHeight(tableHeightForTerminal(msg.Height, m.done))
		m.viewport.Width = maxInt(20, msg.Width-2)
		m.viewport.Height = maxInt(8, msg.Height-7)

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
			m.table.SetRows(resultRows(sortedResultsByAvgLatency(m.liveResults)))
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
		m.table.SetRows(resultRows(sortedResultsByAvgLatency(m.liveResults)))
		return m, nil

	case traceCompleteMsg:
		if m.traceTarget.Endpoint.Code != msg.Code {
			return m, nil
		}
		m.traceError = msg.Err
		m.traceOutput = msg.Output
		m.viewport.SetContent(formatTraceOutput(msg.Output, msg.Err))
		if msg.Err != nil {
			m.traceRunning = false
			return m, nil
		}
		return m, m.waitTraceCmd()

	case traceStoppedMsg:
		m.traceRunning = false
		return m, nil
	}

	if m.mode == traceMode {
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
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
		"%d endpoints | %s | interval %s | timeout %s | family %s | %s | enter traceroute | space pause/resume | q quit",
		len(m.endpoints), durationLabel(m.duration), m.interval, m.timeout, m.family, status,
	)))
	b.WriteString("\n\n")

	if m.mode == traceMode {
		b.WriteString(m.traceView())
	} else if !m.done {
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
		uiMutedStyle.Render("Use arrow keys to scroll. Press Enter to traceroute the selected endpoint, q to quit."),
		"",
		m.table.View(),
	}
	return strings.Join(lines, "\n")
}

func (m tuiModel) traceView() string {
	host := hostOnly(m.traceTarget.Endpoint.Address)
	status := "complete"
	if m.traceRunning {
		status = "running"
	}
	lines := []string{
		uiTitleStyle.Render(fmt.Sprintf("Traceroute: %s (%s)", m.traceTarget.Endpoint.Code, host)),
		uiMutedStyle.Render(fmt.Sprintf("status: %s | esc/backspace return | q quit", status)),
		"",
		m.viewport.View(),
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

func (m tuiModel) selectedResult() (result, bool) {
	row := m.table.SelectedRow()
	if len(row) == 0 {
		return result{}, false
	}
	code := row[0]
	for _, res := range m.liveResults {
		if res.Endpoint.Code == code {
			return res, true
		}
	}
	return result{}, false
}

func startCustomTraceCmd(ctx context.Context, target result, interval, timeout time.Duration, events chan<- traceCompleteMsg) tea.Cmd {
	return func() tea.Msg {
		go func() {
			defer close(events)
			host := hostOnly(target.Endpoint.Address)
			var latest string
			err := runCustomTrace(ctx, host, interval, timeout, func(output string) {
				latest = output
				select {
				case events <- traceCompleteMsg{Code: target.Endpoint.Code, Output: output}:
				case <-ctx.Done():
				}
			})
			if err != nil && ctx.Err() == nil {
				select {
				case events <- traceCompleteMsg{Code: target.Endpoint.Code, Output: latest, Err: err}:
				case <-ctx.Done():
				}
			}
		}()
		return nil
	}
}

func (m tuiModel) waitTraceCmd() tea.Cmd {
	return func() tea.Msg {
		if m.traceEvents == nil {
			return traceStoppedMsg{}
		}
		event, ok := <-m.traceEvents
		if !ok {
			return traceStoppedMsg{}
		}
		return event
	}
}

func (m *tuiModel) stopTrace() {
	if m.traceCancel != nil {
		m.traceCancel()
		m.traceCancel = nil
	}
	m.traceRunning = false
}

func formatTraceOutput(output string, err error) string {
	output = strings.TrimSpace(output)
	if output == "" {
		output = "(no traceroute output)"
	}
	if err != nil {
		if isPermissionError(err) {
			return tracePermissionMessage(err)
		}
		return output + "\n\n" + uiBadStyle.Render("traceroute error: "+err.Error())
	}
	return output
}

func tracePermissionMessage(err error) string {
	var b strings.Builder
	b.WriteString(uiBadStyle.Render("Custom traceroute needs ICMP/raw socket permission."))
	b.WriteString("\n\n")
	b.WriteString("The endpoint ping table still works without this permission, but custom traceroute cannot receive ICMP TTL-exceeded replies or ping intermediate hops.\n\n")
	switch runtime.GOOS {
	case "linux":
		b.WriteString("Linux options:\n")
		b.WriteString("  sudo ./gamelift-ping-report\n")
		b.WriteString("  sudo setcap cap_net_raw+ep ./gamelift-ping-report\n")
	case "darwin":
		b.WriteString("macOS option:\n")
		b.WriteString("  sudo ./gamelift-ping-report\n")
	case "windows":
		b.WriteString("Windows option:\n")
		b.WriteString("  Run the executable from an elevated Administrator terminal.\n")
	default:
		b.WriteString("Run the executable with privileges that allow raw ICMP sockets on this OS.\n")
	}
	b.WriteString("\nUnderlying error: ")
	b.WriteString(err.Error())
	return b.String()
}

func sortedResultsByAvgLatency(results []result) []result {
	sorted := append([]result(nil), results...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return compareResultsByAvgLatency(sorted[i], sorted[j])
	})
	return sorted
}

func compareResultsByAvgLatency(a, b result) bool {
	aUsable := hasUsableLatency(a)
	bUsable := hasUsableLatency(b)
	if aUsable != bUsable {
		return aUsable
	}
	if aUsable && a.Avg != b.Avg {
		return a.Avg < b.Avg
	}
	if a.Loss != b.Loss {
		return a.Loss < b.Loss
	}
	return a.Endpoint.Code < b.Endpoint.Code
}

func hasUsableLatency(res result) bool {
	return res.Error == "" && res.Received > 0 && res.Avg > 0
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

func tableHeightForTerminal(terminalHeight int, done bool) int {
	reservedLines := 5
	if done {
		reservedLines = 8
	}
	return maxInt(8, terminalHeight-reservedLines)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
