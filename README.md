# ghstat

GitHub status at a glance for the repos you care about: the latest GitHub
Actions run of each workflow and the checks on each branch's latest commit,
as a TUI for a tmux popup and a one-shot summary for the shell, plus cached
tmux status-bar badges for check results and open pull requests.

```text
 ghstat  updated 17:56:18  ✗ 2 ⟳ 2 ⚠ 1 ✓ 6

 ▌ ✗ api                     ✗ 2 ⟳ 1 ✓ 3
   ✓ web                     ✓ 2
   ⟳ infrastructure-modules  ⟳ 1 ✓ 1
   ⚠ private-thing           could not fetch: GET /repos/acme/private-thing: 404 Not Found
 ── api · main @ 9f8e7d6 ─────────────────────────────────────
   A ✗ build        failure        3m
   A ⟳ deploy       in_progress    1m
   C ✗ integration  failure        15m
   C ✓ SonarCloud   success        30m
```

`A` is the latest run on the branch of each of the repo's active workflows,
however long ago it ran (re-runs replace earlier attempts; deleted and
disabled workflows, and ones never run on the branch, are left out). `C` is
a check run or commit status (external CI such as Azure Pipelines or
SonarQube) on the branch's latest commit.

## Install

```bash
brew tap moexx399/tap
brew trust --cask moexx399/tap/ghstat   # Homebrew 6+ requires trusting non-official taps
brew install --cask ghstat
```

Or with Go: `go install github.com/moexx399/ghstat@latest`.

## Setup

List repos in `~/.config/ghstat/repos.conf` (or under `$XDG_CONFIG_HOME`,
on macOS too; set `$GHSTAT_CONFIG` for another file), one per line. Without a branch, the repo's default
branch is used:

```text
# owner/repo[:branch]
acme/api
acme/web:develop
```

If that file does not exist, the old `ghws` script's `~/.config/ghws/repos.conf`
is used.

The GitHub token comes from `$GH_TOKEN`, `$GITHUB_TOKEN`, or `gh auth token`
(so being logged in with the GitHub CLI is enough). Set `$GH_HOST` for GitHub
Enterprise Server, or `$GITHUB_API_URL` for any other API root.

## Usage

```text
ghstat [--refresh 1m]     interactive TUI (prints the list when piped)
ghstat list               print every repo's checks once
ghstat badge [--ttl 5m]   print the cached check-results badge for tmux
ghstat badge prs [--since 7d] [--ttl 5m]
                          print the cached open pull request badge for tmux
```

All take `--config FILE` and `--branch BRANCH` (check that branch in every
repo; the PR badge counts open PRs into any branch).

| TUI keys | |
|---|---|
| `↑`/`↓`, `j`/`k` | move |
| `tab`/`enter`, `shift+tab`/`esc` | into / out of a repo's checks |
| `o` | open the selected check, or the repo's Actions page, in the browser |
| `f` | only repos with failing, running or unreachable checks |
| `r` | refresh (also automatic every `--refresh`) |
| `q` | quit |

### tmux

```tmux
# prefix + G: GitHub CI status popup
bind G display-popup -E -w 95% -h 95% ghstat
# Open PRs active in the last week, and check results, in the status bar
set -ag status-right " #(ghstat badge prs --since 7d) #(ghstat badge) "
```

Badges never wait on GitHub: each prints its cached text and, once that is
older than `--ttl`, refreshes it in the background for the next status-bar
tick (one refresh at a time per badge).

- `ghstat badge` shows `GH ✓ ok` when everything passed, otherwise counts of
  failing `✗`, running `⟳` and passing `✓` checks and unreachable repos `⚠`.
  Running `ghstat` or `ghstat list` also updates it.
- `ghstat badge prs` shows `PR 9`: the open pull requests across every repo
  in the file, fetched in one GraphQL request. `--since 36h`, `7d` or `2w`
  counts only PRs updated within that time, leaving out stale ones. Each
  `--since` value has its own cache, so several can sit side by side.

Either shows `⚠ setup` when there is no repos file or token.
