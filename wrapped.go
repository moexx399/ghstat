package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Wrapped is a year in review of the signed-in user's activity across all
// of GitHub, overall and per repo.
type Wrapped struct {
	Login string    `json:"login"`
	Name  string    `json:"name,omitempty"`
	Year  int       `json:"year"`
	From  time.Time `json:"from"`
	To    time.Time `json:"to"`

	Contributions        int    `json:"contributions"` // the profile graph's total
	Commits              int    `json:"commits"`
	PRsOpened            int    `json:"prs_opened"`
	PRsMerged            int    `json:"prs_merged"` // authored PRs merged in the period
	Reviews              int    `json:"reviews"`
	Approvals            int    `json:"approvals"`
	ChangesRequested     int    `json:"changes_requested"`
	BotPRsApproved       int    `json:"bot_prs_approved"` // Dependabot, Renovate and other bot PRs
	Issues               int    `json:"issues_opened"`
	ReposCreated         int    `json:"repos_created"`
	PrivateContributions int    `json:"private_contributions"` // hidden from the token
	Additions            int    `json:"additions"`             // in merged PRs
	Deletions            int    `json:"deletions"`
	MedianMergeSeconds   int64  `json:"median_merge_seconds"`
	BiggestPR            *PRRef `json:"biggest_pr,omitempty"`

	Security  SecurityStats `json:"security"`
	Calendar  CalendarStats `json:"calendar"`
	Languages []NameCount   `json:"languages"` // by commits, from each repo's primary language
	Repos     []RepoWrapped `json:"repos"`     // most active first
	Warnings  []string      `json:"warnings,omitempty"`
}

// RepoWrapped is one repo's share of the year.
type RepoWrapped struct {
	Name              string `json:"name"`
	Private           bool   `json:"private"`
	Language          string `json:"language,omitempty"`
	Commits           int    `json:"commits"`
	PRsOpened         int    `json:"prs_opened"`
	PRsMerged         int    `json:"prs_merged"`
	Reviews           int    `json:"reviews"`
	Approvals         int    `json:"approvals"`
	Issues            int    `json:"issues_opened"`
	Additions         int    `json:"additions"`
	Deletions         int    `json:"deletions"`
	DependabotFixed   int    `json:"dependabot_fixed"`
	CodeScanningFixed int    `json:"code_scanning_fixed"`
	SecretsResolved   int    `json:"secrets_resolved"`
}

func (r RepoWrapped) activity() int {
	return r.Commits + r.PRsOpened + r.PRsMerged + r.Reviews + r.Issues
}

// SecurityFixed is every security alert fixed or resolved in the repo.
func (r RepoWrapped) SecurityFixed() int {
	return r.DependabotFixed + r.CodeScanningFixed + r.SecretsResolved
}

// SecurityStats counts security alerts closed in the period in the repos the
// user contributed to. GitHub does not record who fixed a Dependabot or code
// scanning alert, so those count the repo's fixes; secret scanning alerts
// count only those the user resolved.
type SecurityStats struct {
	DependabotFixed   int            `json:"dependabot_fixed"`
	BySeverity        map[string]int `json:"dependabot_by_severity,omitempty"`
	CodeScanningFixed int            `json:"code_scanning_fixed"`
	SecretsResolved   int            `json:"secrets_resolved"` // by the user
	ReposChecked      int            `json:"repos_checked"`    // at least one kind of alert readable
	ReposUnavailable  int            `json:"repos_unavailable"`
}

func (s SecurityStats) Total() int {
	return s.DependabotFixed + s.CodeScanningFixed + s.SecretsResolved
}

// CalendarStats comes from the contribution graph.
type CalendarStats struct {
	ActiveDays      int     `json:"active_days"`
	LongestStreak   int     `json:"longest_streak"`
	CurrentStreak   int     `json:"current_streak"`
	BusiestDay      string  `json:"busiest_day,omitempty"` // YYYY-MM-DD
	BusiestDayCount int     `json:"busiest_day_count"`
	BusiestWeekday  string  `json:"busiest_weekday,omitempty"`
	Weekdays        [7]int  `json:"weekdays"` // Sunday first
	Months          [12]int `json:"months"`
}

type NameCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type PRRef struct {
	Title     string `json:"title"`
	URL       string `json:"url"`
	Repo      string `json:"repo"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// yearRange is the year, in local time, up to now for the current year.
func yearRange(year int, now time.Time) (from, to time.Time, err error) {
	if year < 2008 || year > now.Year() {
		return from, to, fmt.Errorf("invalid --year %d: want 2008 to %d", year, now.Year())
	}
	from = time.Date(year, 1, 1, 0, 0, 0, 0, now.Location())
	to = time.Date(year, 12, 31, 23, 59, 59, 0, now.Location())
	if to.After(now) {
		to = now
	}
	return from, to, nil
}

// splitWindows splits [from, to] into consecutive windows of at most size,
// so that each window's per-day contribution nodes fit on one page.
func splitWindows(from, to time.Time, size time.Duration) [][2]time.Time {
	var out [][2]time.Time
	for start := from; start.Before(to); start = start.Add(size) {
		end := start.Add(size - time.Second)
		if end.After(to) {
			end = to
		}
		out = append(out, [2]time.Time{start, end})
	}
	return out
}

type gqlRepo struct {
	NameWithOwner   string
	IsPrivate       bool
	PrimaryLanguage *struct{ Name string }
}

const gqlRepoFields = `repository { nameWithOwner isPrivate primaryLanguage { name } }`

type gqlError struct {
	Message string
	Path    []any
}

// graphql runs query and returns its data, and the errors of a partial
// result (fields that failed are null).
func (g *github) graphql(ctx context.Context, query string, vars map[string]any) (json.RawMessage, []gqlError, error) {
	var resp struct {
		Data   json.RawMessage
		Errors []gqlError
	}
	if err := g.post(ctx, g.graphqlURL(), map[string]any{"query": query, "variables": vars}, &resp); err != nil {
		return nil, nil, err
	}
	if len(resp.Data) == 0 || string(resp.Data) == "null" {
		if len(resp.Errors) > 0 {
			return nil, nil, errors.New("GraphQL: " + resp.Errors[0].Message)
		}
		return nil, nil, errors.New("GraphQL: empty response")
	}
	return resp.Data, resp.Errors, nil
}

// wrappedBuilder accumulates a Wrapped while it is fetched.
type wrappedBuilder struct {
	w     *Wrapped
	mu    sync.Mutex
	repos map[string]*RepoWrapped
}

// repo returns the named repo's entry, creating it.
func (b *wrappedBuilder) repo(r gqlRepo) *RepoWrapped {
	e := b.repos[r.NameWithOwner]
	if e == nil {
		e = &RepoWrapped{Name: r.NameWithOwner}
		b.repos[r.NameWithOwner] = e
	}
	e.Private = e.Private || r.IsPrivate
	if r.PrimaryLanguage != nil {
		e.Language = r.PrimaryLanguage.Name
	}
	return e
}

func (b *wrappedBuilder) warn(format string, args ...any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.w.Warnings = append(b.w.Warnings, fmt.Sprintf(format, args...))
}

// wrapped gathers the user's activity between from and to (at most a year).
// Only the contribution summary must succeed; anything else that fails is
// left out and noted in Warnings.
func (g *github) wrapped(ctx context.Context, from, to time.Time) (*Wrapped, error) {
	b := &wrappedBuilder{w: &Wrapped{Year: from.Year(), From: from, To: to}, repos: map[string]*RepoWrapped{}}
	if err := g.wrappedSummary(ctx, b); err != nil {
		return nil, err
	}
	var wg sync.WaitGroup
	var reviews []reviewNode
	var merged []mergedPR
	wg.Go(func() {
		var err error
		if reviews, err = g.wrappedReviews(ctx, from, to); err != nil {
			b.warn("approvals: %v", err)
		}
	})
	wg.Go(func() {
		var total int
		var err error
		merged, total, err = g.wrappedMerged(ctx, b.w.Login, from, to)
		b.w.PRsMerged = total
		if err != nil {
			b.warn("merged PRs: %v", err)
		} else if total > len(merged) {
			b.warn("merged PRs: per-repo counts and line totals cover the %d most recent of %d (search limit)", len(merged), total)
		}
	})
	wg.Wait()

	for _, r := range reviews {
		e := b.repo(r.Repository)
		switch r.PullRequestReview.State {
		case "APPROVED":
			b.w.Approvals++
			e.Approvals++
			if r.PullRequest.Author.Typename == "Bot" {
				b.w.BotPRsApproved++
			}
		case "CHANGES_REQUESTED":
			b.w.ChangesRequested++
		}
	}
	var mergeTimes []time.Duration
	for _, p := range merged {
		e := b.repo(p.Repository)
		e.PRsMerged++
		e.Additions += p.Additions
		e.Deletions += p.Deletions
		b.w.Additions += p.Additions
		b.w.Deletions += p.Deletions
		mergeTimes = append(mergeTimes, p.MergedAt.Sub(p.CreatedAt))
		if big := b.w.BiggestPR; big == nil || p.Additions+p.Deletions > big.Additions+big.Deletions {
			b.w.BiggestPR = &PRRef{Title: p.Title, URL: p.URL, Repo: p.Repository.NameWithOwner, Additions: p.Additions, Deletions: p.Deletions}
		}
	}
	if len(mergeTimes) > 0 {
		slices.Sort(mergeTimes)
		b.w.MedianMergeSeconds = int64(mergeTimes[len(mergeTimes)/2].Seconds())
	}

	g.wrappedSecurity(ctx, b)
	b.finish()
	return b.w, nil
}

// finish orders the repos and tallies languages.
func (b *wrappedBuilder) finish() {
	w := b.w
	langs := map[string]int{}
	for _, r := range b.repos {
		w.Repos = append(w.Repos, *r)
		if r.Language != "" && r.Commits > 0 {
			langs[r.Language] += r.Commits
		}
	}
	sort.Slice(w.Repos, func(i, j int) bool {
		a, c := w.Repos[i], w.Repos[j]
		if a.activity() != c.activity() {
			return a.activity() > c.activity()
		}
		return a.Name < c.Name
	})
	for name, n := range langs {
		w.Languages = append(w.Languages, NameCount{name, n})
	}
	sort.Slice(w.Languages, func(i, j int) bool {
		if w.Languages[i].Count != w.Languages[j].Count {
			return w.Languages[i].Count > w.Languages[j].Count
		}
		return w.Languages[i].Name < w.Languages[j].Name
	})
	slices.Sort(w.Warnings)
}

type repoContribs struct {
	Repository    gqlRepo
	Contributions struct{ TotalCount int }
}

// wrappedSummary reads the contribution totals, the contribution calendar,
// per-repo PR, issue and review counts, and per-repo commit counts (summed
// from per-day nodes over windows short enough to fit on one page).
func (g *github) wrappedSummary(ctx context.Context, b *wrappedBuilder) error {
	w := b.w
	const byRepo = `(maxRepositories: 100) { ` + gqlRepoFields + ` contributions { totalCount } }`
	vars := map[string]any{"from": w.From.Format(time.RFC3339), "to": w.To.Format(time.RFC3339)}
	params := []string{"$from: DateTime!", "$to: DateTime!"}
	var fields []string
	for i, win := range splitWindows(w.From, w.To, 90*24*time.Hour) {
		vars[fmt.Sprintf("f%d", i)] = win[0].Format(time.RFC3339)
		vars[fmt.Sprintf("t%d", i)] = win[1].Format(time.RFC3339)
		params = append(params, fmt.Sprintf("$f%d: DateTime!, $t%[1]d: DateTime!", i))
		fields = append(fields, fmt.Sprintf(`w%d: contributionsCollection(from: $f%[1]d, to: $t%[1]d) { commitContributionsByRepository(maxRepositories: 100) { %s contributions(first: 100) { nodes { commitCount } } } }`, i, gqlRepoFields))
	}
	query := `query(` + strings.Join(params, ", ") + `) { viewer { login name
  all: contributionsCollection(from: $from, to: $to) {
    totalCommitContributions totalIssueContributions totalPullRequestContributions
    totalPullRequestReviewContributions totalRepositoryContributions restrictedContributionsCount
    contributionCalendar { totalContributions weeks { contributionDays { date contributionCount } } }
    pullRequestContributionsByRepository` + byRepo + `
    issueContributionsByRepository` + byRepo + `
    pullRequestReviewContributionsByRepository` + byRepo + `
  }
  ` + strings.Join(fields, "\n  ") + `
} }`
	data, gqlErrs, err := g.graphql(ctx, query, vars)
	if err != nil {
		return err
	}
	var summary struct {
		Viewer struct {
			Login, Name string
			All         *struct {
				TotalCommitContributions            int
				TotalIssueContributions             int
				TotalPullRequestContributions       int
				TotalPullRequestReviewContributions int
				TotalRepositoryContributions        int
				RestrictedContributionsCount        int
				ContributionCalendar                struct {
					TotalContributions int
					Weeks              []struct {
						ContributionDays []calendarDay
					}
				}
				PullRequestContributionsByRepository       []repoContribs
				IssueContributionsByRepository             []repoContribs
				PullRequestReviewContributionsByRepository []repoContribs
			} `json:"all"`
		}
	}
	var windows struct{ Viewer map[string]json.RawMessage }
	if err := cmpOr(json.Unmarshal(data, &summary), json.Unmarshal(data, &windows)); err != nil {
		return err
	}
	all := summary.Viewer.All
	if all == nil {
		if len(gqlErrs) > 0 {
			return errors.New("GraphQL: " + gqlErrs[0].Message)
		}
		return errors.New("GraphQL: no contributions returned")
	}
	w.Login, w.Name = summary.Viewer.Login, summary.Viewer.Name
	w.Contributions = all.ContributionCalendar.TotalContributions
	w.Commits = all.TotalCommitContributions
	w.Issues = all.TotalIssueContributions
	w.PRsOpened = all.TotalPullRequestContributions
	w.Reviews = all.TotalPullRequestReviewContributions
	w.ReposCreated = all.TotalRepositoryContributions
	w.PrivateContributions = all.RestrictedContributionsCount
	var days []calendarDay
	for _, wk := range all.ContributionCalendar.Weeks {
		days = append(days, wk.ContributionDays...)
	}
	w.Calendar = calendarStats(days, w.To)

	for _, c := range all.PullRequestContributionsByRepository {
		b.repo(c.Repository).PRsOpened += c.Contributions.TotalCount
	}
	for _, c := range all.IssueContributionsByRepository {
		b.repo(c.Repository).Issues += c.Contributions.TotalCount
	}
	for _, c := range all.PullRequestReviewContributionsByRepository {
		b.repo(c.Repository).Reviews += c.Contributions.TotalCount
	}
	for alias, raw := range windows.Viewer {
		if len(alias) < 2 || alias[0] != 'w' || alias[1] < '0' || alias[1] > '9' {
			continue
		}
		var win *struct {
			CommitContributionsByRepository []struct {
				Repository    gqlRepo
				Contributions struct{ Nodes []struct{ CommitCount int } }
			}
		}
		if err := json.Unmarshal(raw, &win); err != nil {
			return err
		}
		if win == nil {
			b.warn("per-repo commits: part of the year could not be read")
			continue
		}
		for _, c := range win.CommitContributionsByRepository {
			e := b.repo(c.Repository)
			for _, n := range c.Contributions.Nodes {
				e.Commits += n.CommitCount
			}
		}
	}
	return nil
}

type calendarDay struct {
	Date              string
	ContributionCount int
}

// calendarStats summarises the contribution graph's days up to the day of to.
func calendarStats(days []calendarDay, to time.Time) CalendarStats {
	var s CalendarStats
	last := to.Format(time.DateOnly)
	streak := 0
	var lastDay time.Time
	for _, d := range days {
		if d.Date > last {
			break
		}
		day, err := time.Parse(time.DateOnly, d.Date)
		if err != nil {
			continue
		}
		if !lastDay.IsZero() && day.Sub(lastDay) != 24*time.Hour {
			streak = 0 // a gap in the graph
		}
		lastDay = day
		if d.ContributionCount == 0 {
			streak = 0
			continue
		}
		s.ActiveDays++
		streak++
		s.LongestStreak = max(s.LongestStreak, streak)
		s.Weekdays[day.Weekday()] += d.ContributionCount
		s.Months[day.Month()-1] += d.ContributionCount
		if d.ContributionCount > s.BusiestDayCount {
			s.BusiestDay, s.BusiestDayCount = d.Date, d.ContributionCount
		}
	}
	// The current streak may end yesterday: today is not over yet.
	counts := map[string]int{}
	for _, d := range days {
		counts[d.Date] = d.ContributionCount
	}
	day := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, time.UTC)
	if counts[day.Format(time.DateOnly)] == 0 {
		day = day.AddDate(0, 0, -1)
	}
	for counts[day.Format(time.DateOnly)] > 0 {
		s.CurrentStreak++
		day = day.AddDate(0, 0, -1)
	}
	best := -1
	for i, n := range s.Weekdays {
		if n > 0 && (best < 0 || n > s.Weekdays[best]) {
			best = i
		}
	}
	if best >= 0 {
		s.BusiestWeekday = time.Weekday(best).String()
	}
	return s
}

type reviewNode struct {
	PullRequestReview struct{ State string }
	PullRequest       struct {
		Author struct {
			Typename string `json:"__typename"`
		}
	}
	Repository gqlRepo
}

// wrappedReviews lists every PR review the user submitted in the period.
func (g *github) wrappedReviews(ctx context.Context, from, to time.Time) ([]reviewNode, error) {
	const query = `query($from: DateTime!, $to: DateTime!, $after: String) { viewer {
  contributionsCollection(from: $from, to: $to) {
    pullRequestReviewContributions(first: 100, after: $after) {
      pageInfo { hasNextPage endCursor }
      nodes { pullRequestReview { state } pullRequest { author { __typename } } ` + gqlRepoFields + ` }
    }
  }
} }`
	var out []reviewNode
	vars := map[string]any{"from": from.Format(time.RFC3339), "to": to.Format(time.RFC3339), "after": nil}
	for page := 0; page < 100; page++ {
		data, _, err := g.graphql(ctx, query, vars)
		if err != nil {
			return out, err
		}
		var resp struct {
			Viewer struct {
				ContributionsCollection struct {
					PullRequestReviewContributions struct {
						PageInfo pageInfo
						Nodes    []reviewNode
					}
				}
			}
		}
		if err := json.Unmarshal(data, &resp); err != nil {
			return out, err
		}
		conn := resp.Viewer.ContributionsCollection.PullRequestReviewContributions
		out = append(out, conn.Nodes...)
		if !conn.PageInfo.HasNextPage {
			break
		}
		vars["after"] = conn.PageInfo.EndCursor
	}
	return out, nil
}

type pageInfo struct {
	HasNextPage bool
	EndCursor   string
}

type mergedPR struct {
	Title, URL           string
	Additions, Deletions int
	CreatedAt, MergedAt  time.Time
	Repository           gqlRepo
}

// wrappedMerged lists the user's PRs merged in the period, and their total
// count (search returns at most 1,000).
func (g *github) wrappedMerged(ctx context.Context, login string, from, to time.Time) ([]mergedPR, int, error) {
	const query = `query($q: String!, $after: String) {
  search(query: $q, type: ISSUE, first: 100, after: $after) {
    issueCount
    pageInfo { hasNextPage endCursor }
    nodes { ... on PullRequest { title url additions deletions createdAt mergedAt ` + gqlRepoFields + ` } }
  }
}`
	q := fmt.Sprintf("is:pr is:merged author:%s merged:%s..%s", login, from.Format(time.DateOnly), to.Format(time.DateOnly))
	vars := map[string]any{"q": q, "after": nil}
	var out []mergedPR
	total := 0
	for page := 0; page < 10; page++ {
		data, _, err := g.graphql(ctx, query, vars)
		if err != nil {
			return out, total, err
		}
		var resp struct {
			Search struct {
				IssueCount int
				PageInfo   pageInfo
				Nodes      []mergedPR
			}
		}
		if err := json.Unmarshal(data, &resp); err != nil {
			return out, total, err
		}
		total = resp.Search.IssueCount
		for _, p := range resp.Search.Nodes {
			if p.Repository.NameWithOwner != "" {
				out = append(out, p)
			}
		}
		if !resp.Search.PageInfo.HasNextPage {
			break
		}
		vars["after"] = resp.Search.PageInfo.EndCursor
	}
	return out, total, nil
}

// wrappedSecurity counts, in every repo the user contributed to, the
// Dependabot and code scanning alerts fixed in the period, and the secret
// scanning alerts the user resolved. Repos whose alerts the token cannot read
// (no admin or security access, or the feature is off) are counted as
// unavailable.
func (g *github) wrappedSecurity(ctx context.Context, b *wrappedBuilder) {
	w := b.w
	names := make([]string, 0, len(b.repos))
	for name := range b.repos {
		names = append(names, name)
	}
	slices.Sort(names)
	if len(names) == 0 {
		return
	}
	readable := map[string]bool{}
	var mu sync.Mutex
	ok := func(name string) {
		mu.Lock()
		readable[name] = true
		mu.Unlock()
	}

	var wg sync.WaitGroup
	wg.Go(func() {
		fixed, err := g.dependabotFixed(ctx, names, w.From, w.To)
		if err != nil {
			b.warn("Dependabot alerts: %v", err)
		}
		for name, sev := range fixed {
			ok(name)
			for s, n := range sev {
				b.repos[name].DependabotFixed += n // each repo is written by this goroutine only
				mu.Lock()
				if w.Security.BySeverity == nil {
					w.Security.BySeverity = map[string]int{}
				}
				w.Security.BySeverity[strings.ToLower(s)] += n
				w.Security.DependabotFixed += n
				mu.Unlock()
			}
		}
	})
	sem := make(chan struct{}, 8)
	codeScanning := make([]int, len(names))
	secrets := make([]int, len(names))
	for i, name := range names {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			if n, err := g.codeScanningFixed(ctx, name, w.From, w.To); err == nil {
				codeScanning[i] = n
				ok(name)
			}
			if n, err := g.secretsResolved(ctx, name, w.Login, w.From, w.To); err == nil {
				secrets[i] = n
				ok(name)
			}
		})
	}
	wg.Wait()
	for i, name := range names {
		b.repos[name].CodeScanningFixed = codeScanning[i]
		b.repos[name].SecretsResolved = secrets[i]
		w.Security.CodeScanningFixed += codeScanning[i]
		w.Security.SecretsResolved += secrets[i]
	}
	w.Security.ReposChecked = len(readable)
	w.Security.ReposUnavailable = len(names) - len(readable)
}

// dependabotFixed returns, per readable repo, its Dependabot alerts fixed in
// the period by severity, querying up to 25 repos per request.
func (g *github) dependabotFixed(ctx context.Context, names []string, from, to time.Time) (map[string]map[string]int, error) {
	type alerts struct {
		VulnerabilityAlerts *struct {
			PageInfo pageInfo
			Nodes    []struct {
				FixedAt               *time.Time
				SecurityVulnerability *struct{ Severity string }
			}
		}
	}
	out := map[string]map[string]int{}
	tally := func(name string, a alerts) {
		if out[name] == nil {
			out[name] = map[string]int{}
		}
		for _, n := range a.VulnerabilityAlerts.Nodes {
			if n.FixedAt == nil || n.FixedAt.Before(from) || n.FixedAt.After(to) {
				continue
			}
			sev := "unknown"
			if n.SecurityVulnerability != nil {
				sev = n.SecurityVulnerability.Severity
			}
			out[name][sev]++
		}
	}
	var errs []error
	var more []string // repos with further pages
	cursors := map[string]string{}
	for start := 0; start < len(names); start += 25 {
		chunk := names[start:min(start+25, len(names))]
		vars := map[string]any{}
		params := []string{}
		fields := []string{}
		for i, name := range chunk {
			owner, repo, _ := strings.Cut(name, "/")
			vars[fmt.Sprintf("o%d", i)], vars[fmt.Sprintf("n%d", i)] = owner, repo
			params = append(params, fmt.Sprintf("$o%d: String!, $n%[1]d: String!", i))
			fields = append(fields, fmt.Sprintf("r%d: repository(owner: $o%[1]d, name: $n%[1]d) { %s }", i, fmt.Sprintf(alertsSelection, "")))
		}
		data, _, err := g.graphql(ctx, "query("+strings.Join(params, ", ")+") { "+strings.Join(fields, " ")+" }", vars)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		var resp map[string]*alerts
		if err := json.Unmarshal(data, &resp); err != nil {
			errs = append(errs, err)
			continue
		}
		for i, name := range chunk {
			a := resp[fmt.Sprintf("r%d", i)]
			if a == nil || a.VulnerabilityAlerts == nil {
				continue // no access
			}
			tally(name, *a)
			if a.VulnerabilityAlerts.PageInfo.HasNextPage {
				more = append(more, name)
				cursors[name] = a.VulnerabilityAlerts.PageInfo.EndCursor
			}
		}
	}
	for _, name := range more {
		owner, repo, _ := strings.Cut(name, "/")
		query := `query($o: String!, $n: String!, $after: String) { repository(owner: $o, name: $n) { ` + fmt.Sprintf(alertsSelection, ", after: $after") + ` } }`
		for after := cursors[name]; after != ""; {
			data, _, err := g.graphql(ctx, query, map[string]any{"o": owner, "n": repo, "after": after})
			if err != nil {
				errs = append(errs, err)
				break
			}
			var resp struct{ Repository *alerts }
			if err := json.Unmarshal(data, &resp); err != nil || resp.Repository == nil || resp.Repository.VulnerabilityAlerts == nil {
				break
			}
			tally(name, *resp.Repository)
			after = ""
			if pi := resp.Repository.VulnerabilityAlerts.PageInfo; pi.HasNextPage {
				after = pi.EndCursor
			}
		}
	}
	return out, errors.Join(errs...)
}

// alertsSelection selects a page of a repo's fixed Dependabot alerts; %s
// is further arguments.
const alertsSelection = `vulnerabilityAlerts(states: [FIXED], first: 100%s) { pageInfo { hasNextPage endCursor } nodes { fixedAt securityVulnerability { severity } } }`

// codeScanningFixed counts the repo's code scanning alerts fixed in the
// period, newest-updated first, stopping at alerts last updated before it.
func (g *github) codeScanningFixed(ctx context.Context, repo string, from, to time.Time) (int, error) {
	n := 0
	q := url.Values{"state": {"fixed"}, "sort": {"updated"}, "direction": {"desc"}, "per_page": {"100"}}
	next := "/repos/" + repo + "/code-scanning/alerts?" + q.Encode()
	for next != "" {
		var page []struct {
			FixedAt   *time.Time `json:"fixed_at"`
			UpdatedAt time.Time  `json:"updated_at"`
		}
		var err error
		if next, err = g.get(ctx, next, &page); err != nil {
			return n, err
		}
		for _, a := range page {
			if a.UpdatedAt.Before(from) {
				return n, nil
			}
			if a.FixedAt != nil && !a.FixedAt.Before(from) && !a.FixedAt.After(to) {
				n++
			}
		}
	}
	return n, nil
}

// secretsResolved counts the repo's secret scanning alerts that login
// resolved in the period.
func (g *github) secretsResolved(ctx context.Context, repo, login string, from, to time.Time) (int, error) {
	n := 0
	q := url.Values{"state": {"resolved"}, "sort": {"updated"}, "direction": {"desc"}, "per_page": {"100"}}
	next := "/repos/" + repo + "/secret-scanning/alerts?" + q.Encode()
	for next != "" {
		var page []struct {
			ResolvedAt *time.Time              `json:"resolved_at"`
			UpdatedAt  time.Time               `json:"updated_at"`
			ResolvedBy *struct{ Login string } `json:"resolved_by"`
		}
		var err error
		if next, err = g.get(ctx, next, &page); err != nil {
			return n, err
		}
		for _, a := range page {
			if a.UpdatedAt.Before(from) {
				return n, nil
			}
			if a.ResolvedAt != nil && !a.ResolvedAt.Before(from) && !a.ResolvedAt.After(to) &&
				a.ResolvedBy != nil && strings.EqualFold(a.ResolvedBy.Login, login) {
				n++
			}
		}
	}
	return n, nil
}
