package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// github is a minimal GitHub REST client for the endpoints ghstat reads.
type github struct {
	base  string // API root, e.g. https://api.github.com
	token string
	http  *http.Client

	// olderRuns remembers, per repo, branch and workflow, the latest run of
	// workflows that have not run among the branch's recent runs (nil when
	// never run on it). It stays correct until the workflow runs again, which
	// puts the new run among the recent runs.
	mu        sync.Mutex
	olderRuns map[string]*workflowRun
}

type workflowRun struct {
	WorkflowID               int64 `json:"workflow_id"`
	Name, Status, Conclusion string
	HTMLURL                  string    `json:"html_url"`
	UpdatedAt                time.Time `json:"updated_at"`
}

// newGitHub uses $GH_TOKEN or $GITHUB_TOKEN, else `gh auth token`, for
// $GH_HOST (GitHub Enterprise Server) or github.com. $GITHUB_API_URL, as set
// on GitHub Actions runners, overrides the API root.
func newGitHub(ctx context.Context) (*github, error) {
	host := os.Getenv("GH_HOST")
	base := "https://api.github.com"
	if host != "" && host != "github.com" {
		base = "https://" + host + "/api/v3"
	}
	if u := os.Getenv("GITHUB_API_URL"); u != "" {
		base = strings.TrimRight(u, "/")
	}
	token := os.Getenv("GH_TOKEN")
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	if token == "" {
		args := []string{"auth", "token"}
		if host != "" {
			args = append(args, "--hostname", host)
		}
		out, err := exec.CommandContext(ctx, "gh", args...).Output()
		if err != nil {
			return nil, errors.New("no GitHub token: set GH_TOKEN or log in with 'gh auth login'")
		}
		token = strings.TrimSpace(string(out))
	}
	return &github{base: base, token: token, http: &http.Client{Timeout: 20 * time.Second}}, nil
}

var nextLink = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// get decodes the JSON at path (or an absolute URL) into v and returns the
// next page's URL, if any.
func (g *github) get(ctx context.Context, path string, v any) (string, error) {
	u := path
	if !strings.HasPrefix(path, "http") {
		u = g.base + path
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var body struct{ Message string }
		json.NewDecoder(resp.Body).Decode(&body)
		msg := resp.Status
		if body.Message != "" && !strings.HasSuffix(msg, body.Message) {
			msg += ": " + body.Message
		}
		return "", fmt.Errorf("GET %s: %s", strings.TrimPrefix(u, g.base), msg)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return "", err
	}
	if m := nextLink.FindStringSubmatch(resp.Header.Get("Link")); m != nil {
		return m[1], nil
	}
	return "", nil
}

// fetchAll fetches every repo's status concurrently, in config order.
func (g *github) fetchAll(ctx context.Context, repos []Repo) []RepoStatus {
	out := make([]RepoStatus, len(repos))
	var wg sync.WaitGroup
	for i, r := range repos {
		wg.Go(func() { out[i] = g.fetchRepo(ctx, r) })
	}
	wg.Wait()
	return out
}

// fetchRepo reads the latest run of each workflow on the branch, and the
// check runs and commit statuses on the branch's latest commit. Sources that
// fail are recorded in Errors; the rest are still shown.
func (g *github) fetchRepo(ctx context.Context, r Repo) RepoStatus {
	s := RepoStatus{Repo: r, Branch: r.Branch}
	repoPath := "/repos/" + r.Name
	if s.Branch == "" {
		var info struct {
			DefaultBranch string `json:"default_branch"`
		}
		if _, err := g.get(ctx, repoPath, &info); err != nil {
			s.Errors = append(s.Errors, err.Error())
			return s
		}
		s.Branch, s.DefaultBranch = info.DefaultBranch, true
	}

	var commit struct{ SHA string }
	if _, err := g.get(ctx, repoPath+"/commits/"+url.PathEscape(s.Branch), &commit); err != nil {
		s.Errors = append(s.Errors, err.Error())
		return s
	}
	s.SHA = commit.SHA

	var mu sync.Mutex
	add := func(checks []Check, err error) {
		mu.Lock()
		defer mu.Unlock()
		s.Checks = append(s.Checks, checks...)
		if err != nil {
			s.Errors = append(s.Errors, err.Error())
		}
	}
	var wg sync.WaitGroup
	wg.Go(func() { add(g.actionRuns(ctx, repoPath, s.Branch)) })
	wg.Go(func() { add(g.checkRuns(ctx, repoPath, s.SHA)) })
	wg.Go(func() { add(g.commitStatuses(ctx, repoPath, s.SHA)) })
	wg.Wait()
	sortChecks(s.Checks)
	return s
}

// actionRuns returns the latest run on the branch of every active workflow
// (deleted and disabled ones are left out). Runs are grouped by workflow, so
// renaming a workflow or giving runs their own names does not split it.
// Workflows absent from the branch's 100 most recent runs are looked up one
// by one, once per process.
func (g *github) actionRuns(ctx context.Context, repoPath, branch string) ([]Check, error) {
	var workflows struct {
		Workflows []struct {
			ID          int64
			Name, State string
		}
	}
	var recent struct {
		WorkflowRuns []workflowRun `json:"workflow_runs"`
	}
	var wfErr, runsErr error
	var wg sync.WaitGroup
	wg.Go(func() { _, wfErr = g.get(ctx, repoPath+"/actions/workflows?per_page=100", &workflows) })
	wg.Go(func() {
		q := url.Values{"branch": {branch}, "per_page": {"100"}}
		_, runsErr = g.get(ctx, repoPath+"/actions/runs?"+q.Encode(), &recent)
	})
	wg.Wait()
	if err := cmpOr(wfErr, runsErr); err != nil {
		return nil, err
	}

	latest := map[int64]*workflowRun{}
	for i, r := range recent.WorkflowRuns { // newest first
		if _, ok := latest[r.WorkflowID]; !ok {
			latest[r.WorkflowID] = &recent.WorkflowRuns[i]
		}
	}
	errs := make([]error, len(workflows.Workflows))
	older := make([]*workflowRun, len(workflows.Workflows)) // written by the lookups below
	for i, w := range workflows.Workflows {
		if w.State != "active" || latest[w.ID] != nil {
			continue
		}
		key := fmt.Sprintf("%s@%s#%d", repoPath, branch, w.ID)
		g.mu.Lock()
		run, known := g.olderRuns[key]
		g.mu.Unlock()
		if known {
			latest[w.ID] = run
			continue
		}
		wg.Go(func() {
			var page struct {
				WorkflowRuns []workflowRun `json:"workflow_runs"`
			}
			q := url.Values{"branch": {branch}, "per_page": {"1"}}
			if _, errs[i] = g.get(ctx, fmt.Sprintf("%s/actions/workflows/%d/runs?%s", repoPath, w.ID, q.Encode()), &page); errs[i] != nil {
				return
			}
			var run *workflowRun
			if len(page.WorkflowRuns) > 0 {
				run = &page.WorkflowRuns[0]
			}
			g.mu.Lock()
			defer g.mu.Unlock()
			if g.olderRuns == nil {
				g.olderRuns = map[string]*workflowRun{}
			}
			g.olderRuns[key] = run
			older[i] = run
		})
	}
	wg.Wait()
	for i, w := range workflows.Workflows {
		if older[i] != nil {
			latest[w.ID] = older[i]
		}
	}

	var checks []Check
	for _, w := range workflows.Workflows {
		if r := latest[w.ID]; w.State == "active" && r != nil {
			checks = append(checks, Check{Name: w.Name, Source: SourceActions, Status: r.Status, Conclusion: r.Conclusion, URL: r.HTMLURL, Updated: r.UpdatedAt})
		}
	}
	return checks, cmpOr(errs...)
}

// checkRuns returns the latest check run of each name on the commit.
func (g *github) checkRuns(ctx context.Context, repoPath, sha string) ([]Check, error) {
	latest := map[string]Check{}
	next := repoPath + "/commits/" + sha + "/check-runs?per_page=100"
	for next != "" {
		var page struct {
			CheckRuns []struct {
				Name, Status, Conclusion string
				HTMLURL                  string     `json:"html_url"`
				StartedAt                *time.Time `json:"started_at"`
				CompletedAt              *time.Time `json:"completed_at"`
			} `json:"check_runs"`
		}
		var err error
		if next, err = g.get(ctx, next, &page); err != nil {
			return nil, err
		}
		for _, r := range page.CheckRuns {
			c := Check{Name: r.Name, Source: SourceCommit, Status: r.Status, Conclusion: r.Conclusion, URL: r.HTMLURL}
			if t := cmpOr(r.CompletedAt, r.StartedAt); t != nil {
				c.Updated = *t
			}
			started := c.Updated
			if r.StartedAt != nil {
				started = *r.StartedAt
			}
			if prev, ok := latest[r.Name]; !ok || started.After(prev.started) {
				c.started = started
				latest[r.Name] = c
			}
		}
	}
	return mapValues(latest), nil
}

// commitStatuses returns the latest status of each context on the commit
// (external CI such as Azure Pipelines or SonarQube).
func (g *github) commitStatuses(ctx context.Context, repoPath, sha string) ([]Check, error) {
	var combined struct {
		Statuses []struct {
			Context, State string
			TargetURL      string    `json:"target_url"`
			UpdatedAt      time.Time `json:"updated_at"`
		}
	}
	if _, err := g.get(ctx, repoPath+"/commits/"+sha+"/status", &combined); err != nil {
		return nil, err
	}
	latest := map[string]Check{}
	for _, st := range combined.Statuses {
		if prev, ok := latest[st.Context]; !ok || st.UpdatedAt.After(prev.Updated) {
			latest[st.Context] = Check{Name: st.Context, Source: SourceCommit, Status: st.State, Conclusion: st.State, URL: st.TargetURL, Updated: st.UpdatedAt}
		}
	}
	return mapValues(latest), nil
}

// cmpOr returns the first non-nil value.
func cmpOr[T comparable](vs ...T) T {
	var zero T
	for _, v := range vs {
		if v != zero {
			return v
		}
	}
	return zero
}

func mapValues(m map[string]Check) []Check {
	out := make([]Check, 0, len(m))
	for _, c := range m {
		out = append(out, c)
	}
	return out
}

// sortChecks orders Actions runs before commit checks, each by name.
func sortChecks(cs []Check) {
	slices.SortStableFunc(cs, func(a, b Check) int {
		if a.Source != b.Source {
			return strings.Compare(a.Source, b.Source)
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
}

// PRCount is one repo's open pull request count.
type PRCount struct {
	Repo string
	Open int
	Err  string
}

// openPRs counts each repo's open pull requests, only those updated at or
// after since when it is non-zero, with one GraphQL request holding a search
// per repo.
func (g *github) openPRs(ctx context.Context, repos []Repo, since time.Time) ([]PRCount, error) {
	var names []string
	for _, r := range repos {
		if !slices.Contains(names, r.Name) { // listed once per branch
			names = append(names, r.Name)
		}
	}
	var params, fields []string
	vars := map[string]any{}
	for i, name := range names {
		q := "is:pr is:open repo:" + name
		if !since.IsZero() {
			q += " updated:>=" + since.UTC().Format("2006-01-02T15:04:05+00:00")
		}
		vars[fmt.Sprintf("q%d", i)] = q
		params = append(params, fmt.Sprintf("$q%d: String!", i))
		fields = append(fields, fmt.Sprintf("r%d: search(query: $q%[1]d, type: ISSUE) { issueCount }", i))
	}
	query := "query(" + strings.Join(params, ", ") + ") { " + strings.Join(fields, " ") + " }"

	var resp struct {
		Data   map[string]*struct{ IssueCount int }
		Errors []struct {
			Message string
			Path    []any
		}
	}
	if err := g.post(ctx, g.graphqlURL(), map[string]any{"query": query, "variables": vars}, &resp); err != nil {
		return nil, err
	}
	if resp.Data == nil && len(resp.Errors) > 0 {
		return nil, errors.New(resp.Errors[0].Message)
	}
	counts := make([]PRCount, len(names))
	for i, name := range names {
		counts[i].Repo = name
		if r := resp.Data[fmt.Sprintf("r%d", i)]; r != nil {
			counts[i].Open = r.IssueCount
		} else {
			counts[i].Err = "not found or no access"
		}
	}
	for _, e := range resp.Errors {
		var i int
		if len(e.Path) > 0 {
			if alias, ok := e.Path[0].(string); ok {
				if _, err := fmt.Sscanf(alias, "r%d", &i); err == nil && i < len(counts) {
					counts[i].Err = e.Message
				}
			}
		}
	}
	return counts, nil
}

// graphqlURL is the GraphQL endpoint beside the REST API root.
func (g *github) graphqlURL() string {
	if root, ok := strings.CutSuffix(g.base, "/api/v3"); ok {
		return root + "/api/graphql" // GitHub Enterprise Server
	}
	return g.base + "/graphql"
}

// post sends body as JSON to url and decodes the JSON response into v.
func (g *github) post(ctx context.Context, url string, body, v any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct{ Message string }
		json.NewDecoder(resp.Body).Decode(&e)
		return fmt.Errorf("POST %s: %s %s", strings.TrimPrefix(url, g.base), resp.Status, e.Message)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}
