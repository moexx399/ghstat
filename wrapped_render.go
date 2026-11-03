package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

var (
	wrapTitleSt = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#cba6f7"))
	wrapNumSt   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(hexGreen))
	wrapHeadSt  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(hexBlue))
	wrapBarSt   = lipgloss.NewStyle().Foreground(lipgloss.Color("#f5c2e7"))
)

// renderWrapped is the year in review: overall numbers, highlights, the
// year by month, and a per-repo breakdown of the top repos (all when top
// is 0).
func renderWrapped(w *Wrapped, top, width int) string {
	width = max(width, 60)
	var b strings.Builder
	line := func(s string) { b.WriteString(s + "\n") }
	section := func(title string) {
		line("")
		head := "── " + title + " "
		line(wrapHeadSt.Render(head + strings.Repeat("─", max(width-lipgloss.Width(head)-2, 3))))
	}

	who := "@" + w.Login
	if w.Name != "" {
		who += " (" + w.Name + ")"
	}
	line(wrapTitleSt.Render(fmt.Sprintf("GitHub Wrapped %d", w.Year)) + "  " + boldSt.Render(who))
	line(graySt.Render(w.From.Format("Jan 2") + " – " + w.To.Format("Jan 2, 2006") + ", across all of GitHub"))

	section("Your year in numbers")
	stat := func(n int, label, detail string) {
		s := wrapNumSt.Render(fmt.Sprintf("%8s", commas(n))) + "  " + label
		if detail != "" {
			s += strings.Repeat(" ", max(23-len(label), 1)) + graySt.Render(detail)
		}
		line(s)
	}
	active := 0
	for _, r := range w.Repos {
		if r.activity() > 0 {
			active++
		}
	}
	stat(w.Contributions, "contributions", fmt.Sprintf("on %s active days, in %s repos", commas(w.Calendar.ActiveDays), commas(active)))
	stat(w.Commits, "commits", reposWith(w.Repos, func(r RepoWrapped) int { return r.Commits }))
	stat(w.PRsOpened, "pull requests opened", "")
	merged := ""
	if w.Additions+w.Deletions > 0 {
		merged = fmt.Sprintf("+%s −%s lines", commas(w.Additions), commas(w.Deletions))
	}
	if w.MedianMergeSeconds > 0 {
		merged = joinNonEmpty(" · ", merged, "median "+humanDuration(time.Duration(w.MedianMergeSeconds)*time.Second)+" to merge")
	}
	stat(w.PRsMerged, "pull requests merged", merged)
	stat(w.Reviews, "reviews", joinNonEmpty(" · ",
		plural(w.ChangesRequested, "change request", "change requests"),
		plural(w.BotPRsApproved, "bot PR approved", "bot PRs approved")))
	stat(w.Approvals, "PR approvals", "")
	stat(w.Issues, "issues opened", "")
	if w.ReposCreated > 0 {
		stat(w.ReposCreated, "repos created", "")
	}
	sec := w.Security
	var parts []string
	if sec.DependabotFixed > 0 {
		var sev []string
		for _, s := range []string{"critical", "high", "moderate", "low", "unknown"} {
			if n := sec.BySeverity[s]; n > 0 {
				sev = append(sev, fmt.Sprintf("%d %s", n, s))
			}
		}
		parts = append(parts, fmt.Sprintf("Dependabot %d (%s)", sec.DependabotFixed, strings.Join(sev, ", ")))
	}
	if sec.CodeScanningFixed > 0 {
		parts = append(parts, fmt.Sprintf("code scanning %d", sec.CodeScanningFixed))
	}
	if sec.SecretsResolved > 0 {
		parts = append(parts, fmt.Sprintf("secrets you resolved %d", sec.SecretsResolved))
	}
	switch {
	case sec.ReposChecked == 0 && sec.ReposUnavailable > 0:
		parts = append(parts, "alerts not readable with this token")
	case sec.ReposUnavailable > 0:
		parts = append(parts, fmt.Sprintf("%d of %d repos readable", sec.ReposChecked, sec.ReposChecked+sec.ReposUnavailable))
	}
	stat(sec.Total(), "vulnerabilities fixed", strings.Join(parts, " · "))
	if w.PrivateContributions > 0 {
		line(graySt.Render(fmt.Sprintf("%8s  plus %s private contributions not visible to this token", "", commas(w.PrivateContributions))))
	}

	section("Highlights")
	hl := func(label, value string) {
		if value != "" {
			line("  " + fmt.Sprintf("%-16s", label) + value)
		}
	}
	if r, ok := topRepo(w.Repos, func(r RepoWrapped) int { return r.Commits }); ok {
		hl("Top repo", boldSt.Render(r.Name)+graySt.Render(fmt.Sprintf("  %s commits", commas(r.Commits))))
	}
	if r, ok := topRepo(w.Repos, func(r RepoWrapped) int { return r.Reviews }); ok {
		hl("Most reviewed", boldSt.Render(r.Name)+graySt.Render(fmt.Sprintf("  %s reviews", commas(r.Reviews))))
	}
	if len(w.Languages) > 0 {
		total := 0
		for _, l := range w.Languages {
			total += l.Count
		}
		var ls []string
		for _, l := range w.Languages[:min(3, len(w.Languages))] {
			ls = append(ls, fmt.Sprintf("%s %d%%", l.Name, (l.Count*100+total/2)/total))
		}
		hl("Top languages", strings.Join(ls, " · "))
	}
	c := w.Calendar
	if c.LongestStreak > 0 {
		hl("Longest streak", plural(c.LongestStreak, "day", "days"))
	}
	if c.CurrentStreak > 0 && w.To.Year() == time.Now().Year() {
		hl("Current streak", plural(c.CurrentStreak, "day", "days"))
	}
	if d, err := time.Parse(time.DateOnly, c.BusiestDay); err == nil {
		hl("Busiest day", d.Format("Mon, Jan 2")+graySt.Render(fmt.Sprintf("  %s contributions", commas(c.BusiestDayCount))))
	}
	if c.BusiestWeekday != "" {
		hl("Favorite day", c.BusiestWeekday+"s")
	}
	if p := w.BiggestPR; p != nil {
		hl("Biggest PR", truncate(p.Title, max(width-50, 20))+graySt.Render(fmt.Sprintf("  +%s −%s  %s", commas(p.Additions), commas(p.Deletions), p.URL)))
	}
	if name, why := persona(w); name != "" {
		hl("You are", wrapTitleSt.Render(name)+graySt.Render("  "+why))
	}

	if c.ActiveDays > 0 {
		section("Month by month")
		peak := 0
		for _, n := range c.Months {
			peak = max(peak, n)
		}
		bars := []rune("▁▂▃▄▅▆▇█")
		var spark, labels strings.Builder
		last := 12
		if w.To.Year() == w.Year {
			last = int(w.To.Month())
		}
		for m := 0; m < last; m++ {
			n := c.Months[m]
			ch := " "
			if n > 0 {
				ch = string(bars[min(len(bars)-1, n*len(bars)/(peak+1))])
			}
			spark.WriteString(strings.Repeat(ch, 3) + " ")
			labels.WriteString(time.Month(m + 1).String()[:3] + " ")
		}
		line("  " + wrapBarSt.Render(spark.String()))
		line("  " + graySt.Render(labels.String()))
	}

	if len(w.Repos) > 0 {
		shown := w.Repos
		if top > 0 && len(shown) > top {
			shown = shown[:top]
		}
		title := "By repo"
		if len(shown) < len(w.Repos) {
			title = fmt.Sprintf("By repo (top %d of %d; --top 0 for all)", len(shown), len(w.Repos))
		}
		section(title)
		nameW := 10
		label := func(r RepoWrapped) string {
			if r.Private {
				return r.Name + " 🔒"
			}
			return r.Name
		}
		for _, r := range shown {
			nameW = max(nameW, lipgloss.Width(label(r)))
		}
		nameW = min(nameW, max(width-70, 16))
		cols := []string{"COMMITS", "PRS", "MERGED", "REVIEWS", "APPROVED", "ISSUES", "+LINES", "−LINES", "SEC"}
		head := "  " + fmt.Sprintf("%-*s", nameW, "REPO")
		for _, h := range cols {
			head += " " + padLeft(h, lipgloss.Width(h))
		}
		line(graySt.Render(head))
		for _, r := range shown {
			name := truncate(label(r), nameW)
			row := "  " + boldSt.Render(name) + strings.Repeat(" ", max(nameW-lipgloss.Width(name), 0))
			for i, n := range []int{r.Commits, r.PRsOpened, r.PRsMerged, r.Reviews, r.Approvals, r.Issues, r.Additions, r.Deletions, r.SecurityFixed()} {
				cell := " " + padLeft(commasOrDash(n), lipgloss.Width(cols[i]))
				if n == 0 {
					cell = graySt.Render(cell)
				}
				row += cell
			}
			line(row)
		}
		line(graySt.Render("  PRS opened · MERGED authored PRs merged · SEC security alerts fixed (Dependabot, code scanning) or secrets resolved"))
	}

	for i, warn := range w.Warnings {
		if i == 0 {
			line("")
		}
		line(graySt.Render("⚠ " + warn))
	}
	return b.String()
}

// persona is a light-hearted label for the year, and why.
func persona(w *Wrapped) (string, string) {
	c := w.Calendar
	weekend := c.Weekdays[0] + c.Weekdays[6]
	total := 0
	for _, n := range c.Weekdays {
		total += n
	}
	active := 0
	for _, r := range w.Repos {
		if r.activity() > 0 {
			active++
		}
	}
	switch {
	case total == 0:
		return "", ""
	case w.Security.Total() >= 25:
		return "The Guardian", fmt.Sprintf("%d security alerts fixed", w.Security.Total())
	case w.Reviews >= 10 && w.Reviews >= 2*max(w.PRsOpened, 1):
		return "The Gatekeeper", fmt.Sprintf("%d reviews, %.1f× the PRs you opened", w.Reviews, float64(w.Reviews)/float64(max(w.PRsOpened, 1)))
	case c.LongestStreak >= 30:
		return "The Machine", fmt.Sprintf("a %d-day streak", c.LongestStreak)
	case weekend*100 >= 30*total:
		return "The Weekend Warrior", fmt.Sprintf("%d%% of contributions on weekends", weekend*100/total)
	case active >= 15:
		return "The Explorer", fmt.Sprintf("active in %d repos", active)
	case w.Deletions > w.Additions && w.Deletions >= 1000:
		return "The Minimalist", fmt.Sprintf("deleted %s more lines than you added", commas(w.Deletions-w.Additions))
	}
	return "The Builder", fmt.Sprintf("%s contributions and counting", commas(w.Contributions))
}

func topRepo(repos []RepoWrapped, by func(RepoWrapped) int) (RepoWrapped, bool) {
	var best RepoWrapped
	for _, r := range repos {
		if by(r) > by(best) {
			best = r
		}
	}
	return best, by(best) > 0
}

func reposWith(repos []RepoWrapped, by func(RepoWrapped) int) string {
	n := 0
	for _, r := range repos {
		if by(r) > 0 {
			n++
		}
	}
	if n == 0 {
		return ""
	}
	return "across " + plural(n, "repo", "repos")
}

// commas formats n with thousands separators.
func commas(n int) string {
	s := fmt.Sprint(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		s = "-" + s
	}
	return s
}

func commasOrDash(n int) string {
	if n == 0 {
		return "·"
	}
	return commas(n)
}

// plural is "1 day" or "3 days", or "" for zero.
func plural(n int, one, many string) string {
	switch n {
	case 0:
		return ""
	case 1:
		return "1 " + one
	}
	return commas(n) + " " + many
}

func joinNonEmpty(sep string, parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}

// humanDuration is a rough duration such as 45m, 5h or 3d.
func humanDuration(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", max(int(d.Minutes()), 1))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// padLeft right-aligns s in w display columns.
func padLeft(s string, w int) string {
	return strings.Repeat(" ", max(w-lipgloss.Width(s), 0)) + s
}
