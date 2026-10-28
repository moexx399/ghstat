package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// Sources of a check, shown as a one-letter mark.
const (
	SourceActions = "A" // latest GitHub Actions run of a workflow on the branch
	SourceCommit  = "C" // check run or commit status on the branch's latest commit
)

type Check struct {
	Name, Source       string
	Status, Conclusion string
	URL                string
	Updated            time.Time
	started            time.Time // picks the latest check run of a name
}

// RepoStatus is everything fetched for one configured repo.
type RepoStatus struct {
	Repo
	Branch        string // resolved branch
	DefaultBranch bool   // Branch is the repo's default, so needn't be shown
	SHA           string
	Checks        []Check
	Errors        []string
}

// Label is the repo name, with the branch unless it is the default.
func (s RepoStatus) Label() string {
	_, name, _ := strings.Cut(s.Name, "/")
	if s.Branch != "" && !s.DefaultBranch && s.Branch != "main" {
		name += " (" + s.Branch + ")"
	}
	return name
}

// URL is the repo's Actions page for the branch.
func (s RepoStatus) URL(base string) string {
	web := "https://github.com"
	if strings.HasSuffix(base, "/api/v3") {
		web = strings.TrimSuffix(base, "/api/v3")
	}
	return web + "/" + s.Name + "/actions?query=branch%3A" + s.Branch
}

type state int

const (
	stateSkipped state = iota // neutral, cancelled, skipped
	stateUnknown
	statePassed
	stateRunning
	stateFailed
)

func (c Check) State() state {
	switch c.Conclusion {
	case "success":
		return statePassed
	case "failure", "error", "timed_out", "startup_failure", "stale", "action_required":
		return stateFailed
	case "neutral", "cancelled", "skipped":
		return stateSkipped
	}
	switch c.Status {
	case "in_progress", "queued", "waiting", "pending", "requested":
		return stateRunning
	}
	return stateUnknown
}

// Counts tallies check states.
type Counts struct{ Passed, Failed, Running, Other int }

func (c *Counts) add(s state) {
	switch s {
	case statePassed:
		c.Passed++
	case stateFailed:
		c.Failed++
	case stateRunning:
		c.Running++
	default:
		c.Other++
	}
}

func (s RepoStatus) Counts() Counts {
	var c Counts
	for _, ch := range s.Checks {
		c.add(ch.State())
	}
	return c
}

// Worst is the state that most needs attention, for the repo's glyph.
func (s RepoStatus) Worst() state {
	if len(s.Checks) == 0 {
		return stateUnknown
	}
	w := stateSkipped
	for _, ch := range s.Checks {
		w = max(w, ch.State())
	}
	return w
}

// Catppuccin Mocha, as in the rest of the tmux setup.
const (
	hexGreen  = "#a6e3a1"
	hexRed    = "#f38ba8"
	hexYellow = "#f9e2af"
	hexGray   = "#9399b2"
	hexMuted  = "#6c7086"
	hexBlue   = "#89b4fa"
)

var (
	passSt = lipgloss.NewStyle().Foreground(lipgloss.Color(hexGreen))
	failSt = lipgloss.NewStyle().Foreground(lipgloss.Color(hexRed))
	runSt  = lipgloss.NewStyle().Foreground(lipgloss.Color(hexYellow))
	graySt = lipgloss.NewStyle().Foreground(lipgloss.Color(hexGray))
	boldSt = lipgloss.NewStyle().Bold(true)
)

func (s state) icon() string {
	return map[state]string{statePassed: "✓", stateFailed: "✗", stateSkipped: "⊘", stateRunning: "⟳", stateUnknown: "?"}[s]
}

func (s state) style() lipgloss.Style {
	switch s {
	case statePassed:
		return passSt
	case stateFailed:
		return failSt
	case stateRunning:
		return runSt
	}
	return graySt
}

// renderList is the one-shot CLI summary: a row per repo with its checks,
// wrapped to width under a fixed label column.
func renderList(statuses []RepoStatus, width int) string {
	longest := 0
	for _, s := range statuses {
		longest = max(longest, lipgloss.Width(s.Label()))
	}
	col := min(longest+2, 34, max(width/2, 12))
	indent := strings.Repeat(" ", col)

	var b strings.Builder
	b.WriteString(graySt.Render("Legend: A=actions  C=latest-commit checks/status") + "\n")
	for _, s := range statuses {
		label := s.Label()
		if lipgloss.Width(label) >= col {
			label = string([]rune(label)[:col-2]) + "…"
		}
		line := boldSt.Render(label) + strings.Repeat(" ", col-lipgloss.Width(label))
		switch {
		case len(s.Checks) == 0 && len(s.Errors) > 0:
			b.WriteString(line + graySt.Render(truncate("⚠  could not fetch: "+s.Errors[0], width-col)) + "\n")
			continue
		case len(s.Checks) == 0:
			b.WriteString(line + graySt.Render("—  no runs") + "\n")
			continue
		}
		used := col
		for i, c := range s.Checks {
			st := c.State()
			token := st.style().Render(c.Source+st.icon()) + " " + c.Name
			w := lipgloss.Width(token)
			switch {
			case i == 0:
			case used+2+w > width:
				b.WriteString(line + "\n")
				line, used = indent, col
			default:
				line += "  "
				used += 2
			}
			line += token
			used += w
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// renderBadge is a one-line tmux status-bar summary using #[fg=…] styles.
func renderBadge(statuses []RepoStatus) string {
	var c Counts
	unreachable := 0
	for _, s := range statuses {
		if len(s.Checks) == 0 && len(s.Errors) > 0 {
			unreachable++
		}
		for _, ch := range s.Checks {
			c.add(ch.State())
		}
	}
	out := "#[fg=" + hexMuted + "]GH "
	if c.Failed == 0 && c.Running == 0 && unreachable == 0 {
		out += "#[fg=" + hexGreen + "]✓ ok"
	} else {
		if c.Failed > 0 {
			out += fmt.Sprintf("#[fg=%s]✗ %d ", hexRed, c.Failed)
		}
		if c.Running > 0 {
			out += fmt.Sprintf("#[fg=%s]⟳ %d ", hexYellow, c.Running)
		}
		if unreachable > 0 {
			out += fmt.Sprintf("#[fg=%s]⚠ %d ", hexGray, unreachable)
		}
		out += fmt.Sprintf("#[fg=%s]✓ %d", hexGreen, c.Passed)
	}
	return out + "#[fg=default]"
}

// renderPRBadge is a one-line tmux status-bar count of open pull requests.
func renderPRBadge(counts []PRCount) string {
	open, unreachable := 0, 0
	for _, c := range counts {
		open += c.Open
		if c.Err != "" {
			unreachable++
		}
	}
	color := hexBlue
	if open == 0 {
		color = hexMuted
	}
	out := fmt.Sprintf("#[fg=%s]PR #[fg=%s]%d", hexMuted, color, open)
	if unreachable > 0 {
		out += fmt.Sprintf(" #[fg=%s]⚠ %d", hexGray, unreachable)
	}
	return out + "#[fg=default]"
}

// parseSince parses a lookback such as 36h, 7d or 2w ("" means none).
func parseSince(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	unit := map[byte]time.Duration{'d': 24 * time.Hour, 'w': 7 * 24 * time.Hour}[s[len(s)-1]]
	if unit == 0 {
		d, err := time.ParseDuration(s)
		if err != nil || d <= 0 {
			return 0, fmt.Errorf("invalid --since %q: want e.g. 36h, 7d or 2w", s)
		}
		return d, nil
	}
	var n int
	if _, err := fmt.Sscanf(s[:len(s)-1], "%d", &n); err != nil || n <= 0 || fmt.Sprint(n) != s[:len(s)-1] {
		return 0, fmt.Errorf("invalid --since %q: want e.g. 36h, 7d or 2w", s)
	}
	return time.Duration(n) * unit, nil
}

// ago is a short relative time, e.g. "3m".
func ago(t time.Time, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
