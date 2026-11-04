// Command ghstat shows GitHub Actions and commit check status for a list
// of repos: as a TUI, a one-shot CLI summary, or a cached tmux status badge.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/term"
)

var version = "dev"

func main() {
	err := run(os.Args[1:], os.Stdout, os.Stderr)
	switch {
	case errors.Is(err, flag.ErrHelp):
	case err != nil:
		fmt.Fprintln(os.Stderr, "ghstat:", err)
		os.Exit(1)
	}
}

const usage = `Usage:
  ghstat [flags]              interactive TUI (prints the list when not on a terminal)
  ghstat list [flags]         print every repo's checks once
  ghstat badge [ci] [flags]   cached tmux status-bar badge of check results
  ghstat badge prs [flags]    cached tmux status-bar badge of open PRs (--since 7d)
  ghstat wrapped [flags]      your year on GitHub: commits, PRs, reviews, security fixes
  ghstat version

Repos come from a file with one owner/repo[:branch] per line (default branch
when omitted). The GitHub token is $GH_TOKEN, $GITHUB_TOKEN, or 'gh auth token';
set $GH_HOST for GitHub Enterprise Server.

Flags:
`

func run(args []string, stdout, stderr io.Writer) error {
	cmd := "tui"
	if len(args) > 0 && (args[0] == "list" || args[0] == "badge" || args[0] == "tui" || args[0] == "version" || args[0] == "wrapped") {
		cmd, args = args[0], args[1:]
	}
	kind := "ci"
	if cmd == "badge" && len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		kind, args = args[0], args[1:]
		if kind != "ci" && kind != "prs" {
			return fmt.Errorf("unknown badge %q: want ci or prs", kind)
		}
	}
	if cmd == "version" {
		fmt.Fprintln(stdout, version)
		return nil
	}
	if cmd == "wrapped" {
		return runWrapped(args, stdout, stderr)
	}

	fs := flag.NewFlagSet("ghstat "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	config := fs.String("config", defaultConfigPath(), "repos file")
	branch := fs.String("branch", "", "check this branch in every repo")
	var refreshEvery, ttl time.Duration
	var refresh bool
	var since string
	switch cmd {
	case "tui":
		fs.DurationVar(&refreshEvery, "refresh", time.Minute, "auto-refresh interval (0 to disable)")
	case "badge":
		fs.DurationVar(&ttl, "ttl", 5*time.Minute, "how long a cached badge is fresh")
		fs.BoolVar(&refresh, "refresh", false, "fetch and rewrite the cached badge (run in the background by badge)")
		if kind == "prs" {
			fs.StringVar(&since, "since", "", "only count PRs updated within this long, e.g. 36h, 7d or 2w")
		}
	}
	fs.Usage = func() {
		fmt.Fprint(stderr, usage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	ctx := context.Background()
	if cmd == "badge" {
		lookback, err := parseSince(since)
		if err != nil {
			return err
		}
		b := badgeJob{kind: kind, config: *config, branch: *branch, since: since, lookback: lookback}
		return b.run(ctx, stdout, ttl, refresh)
	}

	repos, err := loadRepos(*config, *branch)
	if err != nil {
		return err
	}
	gh, err := newGitHub(ctx)
	if err != nil {
		return err
	}
	f, isFile := stdout.(*os.File)
	if cmd == "list" || !isFile || !term.IsTerminal(f.Fd()) {
		width := 120
		if isFile {
			if w, _, err := term.GetSize(f.Fd()); err == nil && w > 0 {
				width = w
			}
		}
		statuses := gh.fetchAll(ctx, repos)
		if *branch == "" && *config == defaultConfigPath() {
			writeBadgeCache(statuses)
		}
		_, err := io.WriteString(stdout, renderList(statuses, width))
		return err
	}
	m := newModel(gh, repos, refreshEvery)
	m.updatesBadge = *branch == "" && *config == defaultConfigPath()
	_, err = tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

// badgeJob is one `ghstat badge` invocation.
type badgeJob struct {
	kind, config, branch string
	since                string        // as given, to tell caches apart
	lookback             time.Duration // only PRs updated within this
}

// run prints the cached badge, starting a background refresh when it is
// stale; with refresh, it is that background refresh.
func (b badgeJob) run(ctx context.Context, stdout io.Writer, ttl time.Duration, refresh bool) error {
	name := b.kind
	if b.since != "" {
		name += "-" + b.since
	}
	cache, err := newBadgeCache(name, ttl)
	if err != nil {
		return err
	}
	if refresh {
		unlock, ok := cache.lock(time.Now())
		if !ok {
			return nil // another refresh is running
		}
		defer unlock()
		return cache.write(b.fetch(ctx))
	}
	text, fresh := cache.read(time.Now())
	fmt.Fprint(stdout, text)
	if !fresh {
		args := []string{"badge", b.kind, "--refresh", "--config", b.config, "--branch", b.branch}
		if b.since != "" {
			args = append(args, "--since", b.since)
		}
		return startRefresh(args)
	}
	return nil
}

// fetch renders the badge, or a warning badge when nothing could be
// fetched (no repos file or token), so the status bar shows the problem.
func (b badgeJob) fetch(ctx context.Context) string {
	label := map[string]string{"ci": "GH", "prs": "PR"}[b.kind]
	repos, err := loadRepos(b.config, b.branch)
	if err == nil {
		var gh *github
		if gh, err = newGitHub(ctx); err == nil {
			if b.kind == "ci" {
				return renderBadge(gh.fetchAll(ctx, repos))
			}
			var since time.Time
			if b.lookback > 0 {
				since = time.Now().Add(-b.lookback)
			}
			var counts []PRCount
			if counts, err = gh.openPRs(ctx, repos, since); err == nil {
				return renderPRBadge(counts)
			}
		}
	}
	return "#[fg=" + hexMuted + "]" + label + " #[fg=" + hexRed + "]⚠ setup#[fg=default]"
}

// writeBadgeCache refreshes the badge from statuses fetched for other
// reasons, so the status bar catches up with what the user just saw.
func writeBadgeCache(statuses []RepoStatus) {
	if cache, err := newBadgeCache("ci", 0); err == nil {
		cache.write(renderBadge(statuses))
	}
}

// runWrapped is `ghstat wrapped`: the user's year on GitHub.
func runWrapped(args []string, stdout, stderr io.Writer) error {
	now := time.Now()
	fs := flag.NewFlagSet("ghstat wrapped", flag.ContinueOnError)
	fs.SetOutput(stderr)
	year := fs.Int("year", now.Year(), "the year to look back on (the current one runs to today)")
	top := fs.Int("top", 15, "repos in the per-repo breakdown (0 for all)")
	asJSON := fs.Bool("json", false, "print the numbers as JSON (same as --output json)")
	output := fs.String("output", "text", "output format: text, plain (to paste into a document or chat) or json")
	fs.Usage = func() {
		fmt.Fprint(stderr, "Usage:\n  ghstat wrapped [flags]\n\nYour year across all of GitHub, overall and by repo.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if *top < 0 {
		return fmt.Errorf("invalid --top %d", *top)
	}
	switch *output {
	case "text", "plain", "json":
	default:
		return fmt.Errorf("invalid --output %q: want text, plain or json", *output)
	}
	if *asJSON {
		if *output != "text" && *output != "json" {
			return fmt.Errorf("--json and --output %s conflict", *output)
		}
		*output = "json"
	}
	from, to, err := yearRange(*year, now)
	if err != nil {
		return err
	}
	ctx := context.Background()
	gh, err := newGitHub(ctx)
	if err != nil {
		return err
	}
	f, isFile := stdout.(*os.File)
	tty := isFile && term.IsTerminal(f.Fd())
	if tty && *output != "json" {
		fmt.Fprintf(stderr, "Gathering your %d on GitHub…\r", *year)
	}
	w, err := gh.wrapped(ctx, from, to)
	if tty && *output != "json" {
		fmt.Fprint(stderr, "\r\033[K")
	}
	if err != nil {
		return err
	}
	switch *output {
	case "json":
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(w)
	case "plain":
		_, err = io.WriteString(stdout, renderWrappedPlain(w, *top))
		return err
	}
	width := 100
	if tty {
		if tw, _, err := term.GetSize(f.Fd()); err == nil && tw > 0 {
			width = tw
		}
	}
	_, err = io.WriteString(stdout, renderWrapped(w, *top, width))
	return err
}
