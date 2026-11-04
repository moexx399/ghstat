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
	wrapValueSt = lipgloss.NewStyle()
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

	line(wrapTitleSt.Render(fmt.Sprintf("GitHub Wrapped %d", w.Year)) + "  " + boldSt.Render(wrappedWho(w)))
	line(graySt.Render(wrappedPeriod(w)))

	section("Your year in numbers")
	for _, st := range wrappedStats(w) {
		s := wrapNumSt.Render(fmt.Sprintf("%8s", commas(st.n))) + "  " + st.label
		if st.detail != "" {
			s += strings.Repeat(" ", max(23-len(st.label), 1)) + graySt.Render(st.detail)
		}
		line(s)
	}
	if w.PrivateContributions > 0 {
		line(graySt.Render(fmt.Sprintf("%8s  %s", "", wrappedPrivateNote(w))))
	}

	section("Highlights")
	for _, h := range wrappedHighlights(w, max(width-50, 20)) {
		s := "  " + fmt.Sprintf("%-16s", h.label) + h.style.Render(h.value)
		if h.detail != "" {
			s += graySt.Render("  " + h.detail)
		}
		line(s)
	}

	c := w.Calendar
	if c.ActiveDays > 0 {
		section("Month by month")
		peak := 0
		for _, n := range c.Months {
			peak = max(peak, n)
		}
		bars := []rune("▁▂▃▄▅▆▇█")
		var spark, labels strings.Builder
		for m := 0; m < wrappedMonths(w); m++ {
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
		shown := wrappedTopRepos(w, top)
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
		line(graySt.Render("  PRS opened · MERGED authored PRs merged · SEC Dependabot alerts traced to you, or secrets you resolved"))
	}

	for i, warn := range w.Warnings {
		if i == 0 {
			line("")
		}
		line(graySt.Render("⚠ " + warn))
	}
	return b.String()
}

// renderWrappedPlain is the year in review for pasting into a document or a
// chat: no colors, and no columns or bars that need a monospaced font.
func renderWrappedPlain(w *Wrapped, top int) string {
	var b strings.Builder
	line := func(s string) { b.WriteString(s + "\n") }
	section := func(title string) {
		line("")
		line(title)
	}
	bullet := func(value, detail string) {
		if detail != "" {
			value += " (" + detail + ")"
		}
		line("• " + value)
	}

	line(fmt.Sprintf("GitHub Wrapped %d: %s", w.Year, wrappedWho(w)))
	line(wrappedPeriod(w))

	section("Your year in numbers")
	for _, st := range wrappedStats(w) {
		label := st.label
		if st.n == 1 {
			label = st.one
		}
		bullet(commas(st.n)+" "+label, st.detail)
	}
	if w.PrivateContributions > 0 {
		bullet(wrappedPrivateNote(w), "")
	}

	section("Highlights")
	for _, h := range wrappedHighlights(w, 0) {
		bullet(h.label+": "+h.value, h.detail)
	}

	if w.Calendar.ActiveDays > 0 {
		section("Contributions by month")
		var months []string
		for m := 0; m < wrappedMonths(w); m++ {
			months = append(months, fmt.Sprintf("%s %s", time.Month(m + 1).String()[:3], commas(w.Calendar.Months[m])))
		}
		line(strings.Join(months, " · "))
	}

	if len(w.Repos) > 0 {
		shown := wrappedTopRepos(w, top)
		title := "By repo"
		if len(shown) < len(w.Repos) {
			title = fmt.Sprintf("By repo (top %d of %d)", len(shown), len(w.Repos))
		}
		section(title)
		for _, r := range shown {
			name := r.Name
			if r.Private {
				name += " (private)"
			}
			lines := ""
			if r.Additions+r.Deletions > 0 {
				lines = fmt.Sprintf("+%s −%s lines", commas(r.Additions), commas(r.Deletions))
			}
			line("• " + name + ": " + joinNonEmpty(", ",
				plural(r.Commits, "commit", "commits"),
				plural(r.PRsOpened, "PR opened", "PRs opened"),
				plural(r.PRsMerged, "PR merged", "PRs merged"),
				plural(r.Reviews, "review", "reviews"),
				plural(r.Approvals, "approval", "approvals"),
				plural(r.Issues, "issue opened", "issues opened"),
				lines,
				plural(r.SecurityFixed(), "security fix", "security fixes")))
		}
	}

	for i, warn := range w.Warnings {
		if i == 0 {
			line("")
		}
		line("Note: " + warn)
	}
	return b.String()
}

func wrappedWho(w *Wrapped) string {
	if w.Name != "" {
		return "@" + w.Login + " (" + w.Name + ")"
	}
	return "@" + w.Login
}

func wrappedPeriod(w *Wrapped) string {
	return w.From.Format("Jan 2") + " – " + w.To.Format("Jan 2, 2006") + ", across all of GitHub"
}

func wrappedPrivateNote(w *Wrapped) string {
	return fmt.Sprintf("plus %s private contributions not visible to this token", commas(w.PrivateContributions))
}

// wrappedMonths is how many months of the year have started.
func wrappedMonths(w *Wrapped) int {
	if w.To.Year() == w.Year {
		return int(w.To.Month())
	}
	return 12
}

// wrappedTopRepos is the top repos, or all of them when top is 0.
func wrappedTopRepos(w *Wrapped, top int) []RepoWrapped {
	if top > 0 && len(w.Repos) > top {
		return w.Repos[:top]
	}
	return w.Repos
}

// wrappedStat is one line of the year in numbers.
type wrappedStat struct {
	n                  int
	label, one, detail string
}

func wrappedStats(w *Wrapped) []wrappedStat {
	active := 0
	for _, r := range w.Repos {
		if r.activity() > 0 {
			active++
		}
	}
	merged := ""
	if w.Additions+w.Deletions > 0 {
		merged = fmt.Sprintf("+%s −%s lines", commas(w.Additions), commas(w.Deletions))
	}
	if w.MedianMergeSeconds > 0 {
		merged = joinNonEmpty(" · ", merged, "median "+humanDuration(time.Duration(w.MedianMergeSeconds)*time.Second)+" to merge")
	}
	stats := []wrappedStat{
		{w.Contributions, "contributions", "contribution", fmt.Sprintf("on %s active days, in %s repos", commas(w.Calendar.ActiveDays), commas(active))},
		{w.Commits, "commits", "commit", reposWith(w.Repos, func(r RepoWrapped) int { return r.Commits })},
		{w.PRsOpened, "pull requests opened", "pull request opened", ""},
		{w.PRsMerged, "pull requests merged", "pull request merged", merged},
		{w.Reviews, "reviews", "review", joinNonEmpty(" · ",
			plural(w.ChangesRequested, "change request", "change requests"),
			plural(w.BotPRsApproved, "bot PR approved", "bot PRs approved"))},
		{w.Approvals, "PR approvals", "PR approval", ""},
		{w.Issues, "issues opened", "issue opened", ""},
	}
	if w.ReposCreated > 0 {
		stats = append(stats, wrappedStat{w.ReposCreated, "repos created", "repo created", ""})
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
	if sec.SecretsResolved > 0 {
		parts = append(parts, fmt.Sprintf("secrets you resolved %d", sec.SecretsResolved))
	}
	switch {
	case sec.ReposChecked == 0 && sec.ReposUnavailable > 0:
		parts = append(parts, "alerts not readable with this token")
	case sec.ReposUnavailable > 0:
		parts = append(parts, fmt.Sprintf("%d of %d repos readable", sec.ReposChecked, sec.ReposChecked+sec.ReposUnavailable))
	}
	return append(stats, wrappedStat{sec.Total(), "vulnerabilities fixed", "vulnerability fixed", strings.Join(parts, " · ")})
}

// wrappedHighlight is one line of the highlights; style is for the terminal.
type wrappedHighlight struct {
	label, value, detail string
	style                lipgloss.Style
}

// wrappedHighlights lists the year's highlights. PR titles are cut to
// titleWidth, or left whole when it is 0.
func wrappedHighlights(w *Wrapped, titleWidth int) []wrappedHighlight {
	var hs []wrappedHighlight
	if r, ok := topRepo(w.Repos, func(r RepoWrapped) int { return r.Commits }); ok {
		hs = append(hs, wrappedHighlight{"Top repo", r.Name, fmt.Sprintf("%s commits", commas(r.Commits)), boldSt})
	}
	if r, ok := topRepo(w.Repos, func(r RepoWrapped) int { return r.Reviews }); ok {
		hs = append(hs, wrappedHighlight{"Most reviewed", r.Name, fmt.Sprintf("%s reviews", commas(r.Reviews)), boldSt})
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
		hs = append(hs, wrappedHighlight{"Top languages", strings.Join(ls, " · "), "", wrapValueSt})
	}
	c := w.Calendar
	if c.LongestStreak > 0 {
		hs = append(hs, wrappedHighlight{"Longest streak", plural(c.LongestStreak, "day", "days"), "", wrapValueSt})
	}
	if c.CurrentStreak > 0 && w.To.Year() == time.Now().Year() {
		hs = append(hs, wrappedHighlight{"Current streak", plural(c.CurrentStreak, "day", "days"), "", wrapValueSt})
	}
	if d, err := time.Parse(time.DateOnly, c.BusiestDay); err == nil {
		hs = append(hs, wrappedHighlight{"Busiest day", d.Format("Mon, Jan 2"), fmt.Sprintf("%s contributions", commas(c.BusiestDayCount)), wrapValueSt})
	}
	if c.BusiestWeekday != "" {
		hs = append(hs, wrappedHighlight{"Favorite day", c.BusiestWeekday + "s", "", wrapValueSt})
	}
	if p := w.BiggestPR; p != nil {
		hs = append(hs, wrappedHighlight{"Biggest PR", truncate(p.Title, titleWidth), fmt.Sprintf("+%s −%s · %s", commas(p.Additions), commas(p.Deletions), p.URL), wrapValueSt})
	}
	if name, why := persona(w); name != "" {
		hs = append(hs, wrappedHighlight{"You are", name, why, wrapTitleSt})
	}
	return hs
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
