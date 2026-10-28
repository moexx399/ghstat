package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeGitHub serves the endpoints ghstat reads for two repos:
// acme/api (default branch "trunk", a failing workflow, a paginated set of
// check runs and an external status) and acme/gone (404).
func fakeGitHub(t *testing.T) *httptest.Server {
	t.Helper()
	ts := func(min int) string {
		return time.Now().Add(-time.Duration(min) * time.Minute).UTC().Format(time.RFC3339)
	}
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, v any) { json.NewEncoder(w).Encode(v) }
	mux.HandleFunc("GET /repos/acme/api", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			return
		}
		reply(w, map[string]any{"default_branch": "trunk"})
	})
	mux.HandleFunc("GET /repos/acme/api/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("branch") != "trunk" {
			t.Errorf("runs branch = %q", r.URL.Query().Get("branch"))
		}
		reply(w, map[string]any{"workflow_runs": []map[string]any{
			{"name": "build", "status": "completed", "conclusion": "failure", "html_url": "https://github.com/acme/api/actions/runs/2", "updated_at": ts(3)},
			{"name": "lint", "status": "in_progress", "conclusion": nil, "html_url": "u", "updated_at": ts(1)},
			{"name": "build", "status": "completed", "conclusion": "success", "html_url": "old", "updated_at": ts(60)},
		}})
	})
	mux.HandleFunc("GET /repos/acme/api/commits/trunk", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]any{"sha": "0123456789abcdef"})
	})
	mux.HandleFunc("GET /repos/acme/api/commits/0123456789abcdef/check-runs", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "" {
			w.Header().Set("Link", fmt.Sprintf(`<http://%s%s?per_page=100&page=2>; rel="next", <x>; rel="last"`, r.Host, r.URL.Path))
			reply(w, map[string]any{"check_runs": []map[string]any{
				{"name": "test", "status": "completed", "conclusion": "failure", "html_url": "old-test", "started_at": ts(30), "completed_at": ts(25)},
			}})
			return
		}
		reply(w, map[string]any{"check_runs": []map[string]any{
			{"name": "test", "status": "completed", "conclusion": "success", "html_url": "new-test", "started_at": ts(10), "completed_at": ts(5)},
		}})
	})
	mux.HandleFunc("GET /repos/acme/api/commits/0123456789abcdef/status", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]any{"statuses": []map[string]any{
			{"context": "sonar", "state": "pending", "target_url": "https://sonar", "updated_at": ts(2)},
		}})
	})
	mux.HandleFunc("GET /repos/acme/gone", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	})
	// Open PR counts: acme/api has 3 open PRs, 1 updated recently; acme/gone
	// cannot be searched.
	mux.HandleFunc("POST /graphql", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string
			Variables map[string]string
		}
		json.NewDecoder(r.Body).Decode(&req)
		data, errs := map[string]any{}, []map[string]any{}
		for k, q := range req.Variables {
			alias := "r" + strings.TrimPrefix(k, "q")
			if !strings.Contains(req.Query, alias+": search(query: $"+k+", type: ISSUE)") {
				t.Errorf("query %q lacks %s", req.Query, alias)
			}
			switch {
			case strings.Contains(q, "repo:acme/gone"):
				data[alias] = nil
				errs = append(errs, map[string]any{"path": []string{alias}, "message": "cannot be searched"})
			case strings.Contains(q, "updated:>="):
				data[alias] = map[string]int{"issueCount": 1}
			default:
				data[alias] = map[string]int{"issueCount": 3}
			}
		}
		reply(w, map[string]any{"data": data, "errors": errs})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestOpenPRs(t *testing.T) {
	srv := fakeGitHub(t)
	gh := &github{base: srv.URL, token: "test-token", http: srv.Client()}
	repos := []Repo{{Name: "acme/api"}, {Name: "acme/api", Branch: "release"}, {Name: "acme/gone"}}
	counts, err := gh.openPRs(context.Background(), repos, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(counts) != 2 || counts[0] != (PRCount{Repo: "acme/api", Open: 3}) || counts[1].Err != "cannot be searched" {
		t.Fatalf("counts %+v", counts)
	}
	if got := renderPRBadge(counts); got != "#[fg=#6c7086]PR #[fg=#89b4fa]3 #[fg=#9399b2]⚠ 1#[fg=default]" {
		t.Fatalf("badge %q", got)
	}
	recent, err := gh.openPRs(context.Background(), repos[:1], time.Now().Add(-7*24*time.Hour))
	if err != nil || recent[0].Open != 1 {
		t.Fatalf("since: %+v %v", recent, err)
	}
	if got := renderPRBadge([]PRCount{{Repo: "a/b"}}); got != "#[fg=#6c7086]PR #[fg=#6c7086]0#[fg=default]" {
		t.Fatalf("zero badge %q", got)
	}
	if (&github{base: "https://ghe.example.com/api/v3"}).graphqlURL() != "https://ghe.example.com/api/graphql" {
		t.Fatal("GHES GraphQL URL")
	}
}

func TestParseSince(t *testing.T) {
	for in, want := range map[string]time.Duration{"": 0, "36h": 36 * time.Hour, "7d": 7 * 24 * time.Hour, "2w": 14 * 24 * time.Hour, "90m": 90 * time.Minute} {
		if got, err := parseSince(in); err != nil || got != want {
			t.Errorf("parseSince(%q) = %v, %v", in, got, err)
		}
	}
	for _, in := range []string{"7", "d", "-3d", "1.5d", "7days", "0w"} {
		if _, err := parseSince(in); err == nil {
			t.Errorf("parseSince(%q) succeeded", in)
		}
	}
}

func TestBadgeArgs(t *testing.T) {
	var out, errOut strings.Builder
	if err := run([]string{"badge", "issues"}, &out, &errOut); err == nil || !strings.Contains(err.Error(), "want ci or prs") {
		t.Fatalf("unknown kind: %v", err)
	}
	if err := run([]string{"badge", "--since", "7d"}, &out, &errOut); err == nil {
		t.Fatal("--since should only apply to the prs badge")
	}
	if err := run([]string{"badge", "prs", "--since", "soon"}, &out, &errOut); err == nil || !strings.Contains(err.Error(), "invalid --since") {
		t.Fatalf("bad since: %v", err)
	}
}

func TestFetchAll(t *testing.T) {
	srv := fakeGitHub(t)
	gh := &github{base: srv.URL, token: "test-token", http: srv.Client()}
	got := gh.fetchAll(context.Background(), []Repo{{Name: "acme/api"}, {Name: "acme/gone"}})

	api := got[0]
	if api.Branch != "trunk" || !api.DefaultBranch || api.SHA != "0123456789abcdef" || len(api.Errors) != 0 {
		t.Fatalf("api: %+v", api)
	}
	var names []string
	for _, c := range api.Checks {
		names = append(names, fmt.Sprintf("%s:%s:%v", c.Source, c.Name, c.State()))
	}
	want := []string{
		fmt.Sprintf("A:build:%v", stateFailed),  // the newest run wins
		fmt.Sprintf("A:lint:%v", stateRunning),  //
		fmt.Sprintf("C:sonar:%v", stateRunning), // commit status
		fmt.Sprintf("C:test:%v", statePassed),   // latest check run, from page 2
	}
	if strings.Join(names, " ") != strings.Join(want, " ") {
		t.Fatalf("checks %v\nwant   %v", names, want)
	}
	if c := api.Counts(); c != (Counts{Passed: 1, Failed: 1, Running: 2}) {
		t.Fatalf("counts %+v", c)
	}
	if got[1].Label() != "gone" || len(got[1].Checks) != 0 || !strings.Contains(got[1].Errors[0], "404") {
		t.Fatalf("gone: %+v", got[1])
	}

	// A bad token is reported, not hidden.
	bad := (&github{base: srv.URL, token: "nope", http: srv.Client()}).fetchRepo(context.Background(), Repo{Name: "acme/api"})
	if len(bad.Errors) != 1 || !strings.Contains(bad.Errors[0], "Bad credentials") {
		t.Fatalf("bad token: %+v", bad)
	}
}

func TestRenderListAndBadge(t *testing.T) {
	srv := fakeGitHub(t)
	gh := &github{base: srv.URL, token: "test-token", http: srv.Client()}
	statuses := gh.fetchAll(context.Background(), []Repo{{Name: "acme/api"}, {Name: "acme/gone", Branch: "release/1"}})

	list := renderList(statuses, 40)
	for _, s := range []string{"Legend: A=actions", "api", "A✗ build", "C✓ test", "gone (release/1)", "⚠  could not fetch"} {
		if !strings.Contains(list, s) {
			t.Errorf("list missing %q:\n%s", s, list)
		}
	}
	// Wrapped under the label column at width 40.
	if lines := strings.Split(strings.TrimSpace(list), "\n"); len(lines) < 4 || !strings.HasPrefix(lines[2], strings.Repeat(" ", 10)) {
		t.Errorf("list not wrapped:\n%s", list)
	}

	want := "#[fg=#6c7086]GH #[fg=#f38ba8]✗ 1 #[fg=#f9e2af]⟳ 2 #[fg=#9399b2]⚠ 1 #[fg=#a6e3a1]✓ 1#[fg=default]"
	if got := renderBadge(statuses); got != want {
		t.Fatalf("badge\n got %q\nwant %q", got, want)
	}
	if got := renderBadge(statuses[:0]); got != "#[fg=#6c7086]GH #[fg=#a6e3a1]✓ ok#[fg=default]" {
		t.Fatalf("empty badge %q", got)
	}
}

func TestLoadRepos(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repos.conf")
	os.WriteFile(path, []byte("# mine\nacme/api\n\nacme/web:develop  # trailing comment\n"), 0o644)
	repos, err := loadRepos(path, "")
	if err != nil || len(repos) != 2 || repos[0] != (Repo{"acme/api", ""}) || repos[1] != (Repo{"acme/web", "develop"}) {
		t.Fatalf("repos %+v, %v", repos, err)
	}
	if repos, _ := loadRepos(path, "hotfix"); repos[0].Branch != "hotfix" || repos[1].Branch != "hotfix" {
		t.Fatalf("branch override: %+v", repos)
	}
	os.WriteFile(path, []byte("not-a-repo\n"), 0o644)
	if _, err := loadRepos(path, ""); err == nil || !strings.Contains(err.Error(), ":1:") {
		t.Fatalf("bad line: %v", err)
	}
	if _, err := loadRepos(filepath.Join(t.TempDir(), "missing"), ""); err == nil || !strings.Contains(err.Error(), "no repos file") {
		t.Fatalf("missing: %v", err)
	}
}

func TestBadgeCache(t *testing.T) {
	c := badgeCache{path: filepath.Join(t.TempDir(), "ghstat", "badge"), ttl: time.Minute}
	now := time.Now()
	if text, fresh := c.read(now); text != "" || fresh {
		t.Fatal("empty cache should be stale")
	}
	if err := c.write("GH ok"); err != nil {
		t.Fatal(err)
	}
	if text, fresh := c.read(now); text != "GH ok" || !fresh {
		t.Fatalf("read %q fresh=%v", text, fresh)
	}
	if _, fresh := c.read(now.Add(2 * time.Minute)); fresh {
		t.Fatal("old cache should be stale")
	}

	unlock, ok := c.lock(now)
	if !ok {
		t.Fatal("first lock failed")
	}
	if _, ok := c.lock(now); ok {
		t.Fatal("second lock should fail while held")
	}
	if _, ok := c.lock(now.Add(refreshTimeout + time.Second)); !ok {
		t.Fatal("abandoned lock should be taken over")
	}
	unlock()
}

func TestStates(t *testing.T) {
	for _, tc := range []struct {
		conclusion, status string
		want               state
	}{
		{"success", "completed", statePassed},
		{"timed_out", "completed", stateFailed},
		{"action_required", "completed", stateFailed},
		{"skipped", "completed", stateSkipped},
		{"", "queued", stateRunning},
		{"pending", "pending", stateRunning}, // commit status
		{"error", "error", stateFailed},      // commit status
		{"", "", stateUnknown},
	} {
		if got := (Check{Conclusion: tc.conclusion, Status: tc.status}).State(); got != tc.want {
			t.Errorf("%s/%s = %v, want %v", tc.conclusion, tc.status, got, tc.want)
		}
	}
}
