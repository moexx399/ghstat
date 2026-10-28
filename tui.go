package main

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	titleSt  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#1e1e2e")).Background(lipgloss.Color(hexGreen)).Padding(0, 1)
	headSt   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(hexBlue))
	cursorSt = lipgloss.NewStyle().Foreground(lipgloss.Color("#cba6f7")).Bold(true)
	appSt    = lipgloss.NewStyle().Padding(1, 2)
)

type pane int

const (
	reposPane pane = iota
	checksPane
)

type (
	statusesMsg struct {
		statuses []RepoStatus
		at       time.Time
	}
	tickMsg   struct{}
	openedMsg struct{ err error }
)

type model struct {
	gh    *github
	repos []Repo
	every time.Duration
	// updatesBadge refreshes the tmux badge cache with each fetch; only for
	// the default repos file and branches, which the badge shows.
	updatesBadge bool
	statuses     []RepoStatus
	at           time.Time
	loading      bool
	spin         spinner.Model
	err          error

	focus       pane
	cursor      int // index into visible()
	checkCursor int
	attention   bool // only repos with failing, running or unreachable checks

	width, height int
}

func newModel(gh *github, repos []Repo, every time.Duration) model {
	return model{gh: gh, repos: repos, every: every,
		spin: spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(runSt))}
}

func (m model) Init() tea.Cmd { return m.fetch() }

func (m *model) fetch() tea.Cmd {
	m.loading = true
	gh, repos := m.gh, m.repos
	return tea.Batch(m.spin.Tick, func() tea.Msg {
		s := gh.fetchAll(context.Background(), repos)
		return statusesMsg{s, time.Now()}
	})
}

// visible is the repos shown, honouring the attention filter.
func (m model) visible() []RepoStatus {
	if !m.attention {
		return m.statuses
	}
	var out []RepoStatus
	for _, s := range m.statuses {
		if w := s.Worst(); w == stateFailed || w == stateRunning || len(s.Checks) == 0 && len(s.Errors) > 0 {
			out = append(out, s)
		}
	}
	return out
}

func (m model) selected() (RepoStatus, bool) {
	v := m.visible()
	if m.cursor < len(v) {
		return v[m.cursor], true
	}
	return RepoStatus{}, false
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case spinner.TickMsg:
		if m.loading {
			var cmd tea.Cmd
			m.spin, cmd = m.spin.Update(msg)
			return m, cmd
		}
	case statusesMsg:
		m.loading, m.statuses, m.at = false, msg.statuses, msg.at
		if m.updatesBadge {
			writeBadgeCache(msg.statuses)
		}
		m.cursor = min(m.cursor, max(0, len(m.visible())-1))
		m.clampChecks()
		if m.every > 0 {
			return m, tea.Tick(m.every, func(time.Time) tea.Msg { return tickMsg{} })
		}
	case tickMsg:
		if !m.loading {
			return m, m.fetch()
		}
	case openedMsg:
		m.err = msg.err
	case tea.KeyMsg:
		return m.key(msg.String())
	}
	return m, nil
}

func (m *model) clampChecks() {
	s, _ := m.selected()
	m.checkCursor = min(m.checkCursor, max(0, len(s.Checks)-1))
}

func (m model) key(k string) (tea.Model, tea.Cmd) {
	m.err = nil
	switch k {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "esc":
		if m.focus == checksPane {
			m.focus = reposPane
			return m, nil
		}
		return m, tea.Quit
	case "r":
		if !m.loading {
			return m, m.fetch()
		}
	case "f":
		m.attention = !m.attention
		m.cursor, m.checkCursor, m.focus = 0, 0, reposPane
	case "tab", "right", "l", "enter":
		if s, ok := m.selected(); ok && len(s.Checks) > 0 && m.focus == reposPane {
			m.focus = checksPane
		}
	case "shift+tab", "left", "h":
		m.focus = reposPane
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "pgup":
		m.move(-10)
	case "pgdown":
		m.move(10)
	case "home", "g":
		m.move(-1 << 20)
	case "end", "G":
		m.move(1 << 20)
	case "o":
		s, ok := m.selected()
		if !ok {
			return m, nil
		}
		url := s.URL(m.gh.base)
		if m.focus == checksPane && m.checkCursor < len(s.Checks) && s.Checks[m.checkCursor].URL != "" {
			url = s.Checks[m.checkCursor].URL
		}
		return m, openURL(url)
	}
	return m, nil
}

func (m *model) move(d int) {
	if m.focus == checksPane {
		s, _ := m.selected()
		m.checkCursor = max(0, min(m.checkCursor+d, len(s.Checks)-1))
		return
	}
	m.cursor = max(0, min(m.cursor+d, len(m.visible())-1))
	m.checkCursor = 0
}

func openURL(url string) tea.Cmd {
	return func() tea.Msg {
		name := "xdg-open"
		if runtime.GOOS == "darwin" {
			name = "open"
		}
		cmd := exec.Command(name, url)
		if err := cmd.Start(); err != nil {
			return openedMsg{fmt.Errorf("open %s: %w", url, err)}
		}
		go cmd.Wait()
		return openedMsg{}
	}
}

func (m model) View() string {
	w, h := appSt.GetFrameSize()
	width, height := max(20, m.width-w), max(8, m.height-h)

	var total Counts
	unreachable := 0
	for _, s := range m.statuses {
		c := s.Counts()
		total.Passed, total.Failed, total.Running = total.Passed+c.Passed, total.Failed+c.Failed, total.Running+c.Running
		if len(s.Checks) == 0 && len(s.Errors) > 0 {
			unreachable++
		}
	}
	header := titleSt.Render("ghstat") + " "
	switch {
	case m.statuses == nil:
		return appSt.Render(header + m.spin.View() + fmt.Sprintf(" Fetching %d repos...", len(m.repos)))
	case m.loading:
		header += m.spin.View() + graySt.Render(" refreshing  ")
	default:
		header += graySt.Render("updated " + m.at.Format("15:04:05") + "  ")
	}
	header += summary(total, unreachable)
	if m.attention {
		header += graySt.Render("  · needs attention only")
	}

	help := graySt.Render("↑/↓ move • tab checks • o open in browser • f needs attention • r refresh • q quit")
	if m.err != nil {
		help = failSt.Render(m.err.Error())
	}

	visible := m.visible()
	s, ok := m.selected()
	body := height - 4 // header, blank, detail title, help
	repoRows := min(max(len(visible), 1), max(3, body/2))
	checkRows := max(1, body-repoRows)

	var b strings.Builder
	b.WriteString(header + "\n\n")
	b.WriteString(m.renderRepos(visible, repoRows, width))
	title := "No repos need attention"
	if ok {
		title = s.Label() + graySt.Render(" · "+s.Branch)
		if len(s.SHA) >= 7 {
			title += graySt.Render(" @ " + s.SHA[:7])
		}
	}
	b.WriteString(headSt.Render("── ") + headSt.Render(title) + " " + headSt.Render(strings.Repeat("─", max(0, min(width, 90)-lipgloss.Width(title)-4))) + "\n")
	b.WriteString(m.renderChecks(s, checkRows, width))
	b.WriteString(help)
	return appSt.Render(b.String())
}

func summary(c Counts, unreachable int) string {
	parts := []string{}
	if c.Failed > 0 {
		parts = append(parts, failSt.Render(fmt.Sprintf("✗ %d", c.Failed)))
	}
	if c.Running > 0 {
		parts = append(parts, runSt.Render(fmt.Sprintf("⟳ %d", c.Running)))
	}
	if unreachable > 0 {
		parts = append(parts, graySt.Render(fmt.Sprintf("⚠ %d", unreachable)))
	}
	parts = append(parts, passSt.Render(fmt.Sprintf("✓ %d", c.Passed)))
	return strings.Join(parts, " ")
}

// window returns the first row to show so that cursor is visible.
func window(cursor, rows int) int { return max(0, cursor-rows+1) }

func (m model) renderRepos(visible []RepoStatus, rows, width int) string {
	if len(visible) == 0 {
		return graySt.Render("  Nothing failing or running.") + "\n" + strings.Repeat("\n", rows-1)
	}
	labelW := 0
	for _, s := range visible {
		labelW = max(labelW, lipgloss.Width(s.Label()))
	}
	labelW = min(labelW, max(12, width/2))
	var b strings.Builder
	start := window(m.cursor, rows)
	for i := start; i < start+rows; i++ {
		if i >= len(visible) {
			b.WriteString("\n")
			continue
		}
		s := visible[i]
		worst := s.Worst()
		glyph := worst.style().Render(worst.icon())
		detail := summary(s.Counts(), 0)
		if len(s.Checks) == 0 {
			glyph, detail = graySt.Render("⚠"), graySt.Render("no runs")
			if len(s.Errors) > 0 {
				detail = graySt.Render(truncate("could not fetch: "+s.Errors[0], width-labelW-8))
			}
		}
		label := truncate(s.Label(), labelW)
		line := glyph + " " + label + strings.Repeat(" ", labelW-lipgloss.Width(label)) + "  " + detail
		if i == m.cursor {
			pointer := cursorSt.Render("▌ ")
			if m.focus == checksPane {
				pointer = graySt.Render("▌ ")
			}
			b.WriteString(pointer + boldSt.Render(line) + "\n")
		} else {
			b.WriteString("  " + line + "\n")
		}
	}
	return b.String()
}

func (m model) renderChecks(s RepoStatus, rows, width int) string {
	var b strings.Builder
	if len(s.Checks) == 0 {
		for _, e := range s.Errors {
			b.WriteString(failSt.Render(truncate("  "+e, width)) + "\n")
			rows--
		}
		return b.String() + strings.Repeat("\n", max(0, rows))
	}
	nameW := 0
	for _, c := range s.Checks {
		nameW = max(nameW, lipgloss.Width(c.Name))
	}
	nameW = min(nameW, max(12, width-30))
	now := time.Now()
	start := window(m.checkCursor, rows)
	for i := start; i < start+rows; i++ {
		if i >= len(s.Checks) {
			b.WriteString("\n")
			continue
		}
		c := s.Checks[i]
		st := c.State()
		result := c.Conclusion
		if result == "" {
			result = c.Status
		}
		name := truncate(c.Name, nameW)
		line := graySt.Render(c.Source) + " " + st.style().Render(st.icon()) + " " + name +
			strings.Repeat(" ", nameW-lipgloss.Width(name)) + "  " + st.style().Render(fmt.Sprintf("%-15s", result)) + graySt.Render(ago(c.Updated, now))
		if m.focus == checksPane && i == m.checkCursor {
			b.WriteString(cursorSt.Render("▌ ") + line + "\n")
		} else {
			b.WriteString("  " + line + "\n")
		}
	}
	return b.String()
}

func truncate(s string, w int) string {
	if w <= 1 || lipgloss.Width(s) <= w {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r)) > w-1 {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}
