package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeWrapped serves the GraphQL and REST endpoints `ghstat wrapped` reads,
// for user octo in 2025: commits to acme/api and the private acme/web,
// reviews (paginated) in acme/api and other/lib, two merged PRs, and
// security alerts some of which the token cannot read. With hidden, the
// contribution graph is empty and search also finds the private acme/secret,
// as when the graph leaves private repos out.
func fakeWrapped(t *testing.T, hidden bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, v any) { json.NewEncoder(w).Encode(v) }
	repo := func(name, lang string, private bool) map[string]any {
		r := map[string]any{"nameWithOwner": name, "isPrivate": private, "primaryLanguage": nil}
		if lang != "" {
			r["primaryLanguage"] = map[string]any{"name": lang}
		}
		return r
	}
	api, web, lib := repo("acme/api", "Go", false), repo("acme/web", "TypeScript", true), repo("other/lib", "", false)
	secret := repo("acme/secret", "", true)
	withRepo := func(r map[string]any, n int) []any {
		var out []any
		for range n {
			out = append(out, map[string]any{"repository": r})
		}
		return out
	}
	searchReply := func(nodes []any) map[string]any {
		return map[string]any{"data": map[string]any{"search": map[string]any{"issueCount": len(nodes), "pageInfo": map[string]any{"hasNextPage": false}, "nodes": nodes}}}
	}
	days := func(counts map[string]int) []map[string]any {
		var out []map[string]any
		for d := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC); d.Year() == 2025; d = d.AddDate(0, 0, 1) {
			out = append(out, map[string]any{"date": d.Format(time.DateOnly), "contributionCount": counts[d.Format(time.DateOnly)]})
		}
		return out
	}
	mux.HandleFunc("POST /graphql", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string
			Variables map[string]any
		}
		json.NewDecoder(r.Body).Decode(&req)
		q, vars := req.Query, req.Variables
		switch {
		case strings.Contains(q, "all: contributionsCollection"):
			if !strings.Contains(q, "w4:") || strings.Contains(q, "w5:") {
				t.Errorf("want 5 windows of 90 days:\n%s", q)
			}
			if vars["from"] != "2025-01-01T00:00:00Z" || vars["t4"] != "2025-12-31T23:59:59Z" {
				t.Errorf("vars %v", vars)
			}
			commits := func(r map[string]any, n ...int) map[string]any {
				var nodes []map[string]int
				for _, c := range n {
					nodes = append(nodes, map[string]int{"commitCount": c})
				}
				return map[string]any{"repository": r, "contributions": map[string]any{"nodes": nodes}}
			}
			count := func(r map[string]any, n int) map[string]any {
				return map[string]any{"repository": r, "contributions": map[string]int{"totalCount": n}}
			}
			viewer := map[string]any{
				"login": "octo", "name": "Octo Cat",
				"all": map[string]any{
					"totalCommitContributions": 10, "totalIssueContributions": 2, "totalPullRequestContributions": 5,
					"totalPullRequestReviewContributions": 10, "totalRepositoryContributions": 1, "restrictedContributionsCount": 4,
					"contributionCalendar": map[string]any{"totalContributions": 20, "weeks": []map[string]any{{"contributionDays": days(map[string]int{
						"2025-01-01": 1, "2025-01-02": 2, "2025-01-04": 3, "2025-01-05": 4, "2025-03-04": 10, "2025-12-30": 1, "2025-12-31": 1,
					})}}},
					"pullRequestContributionsByRepository":       []any{count(api, 5)},
					"issueContributionsByRepository":             []any{count(web, 2)},
					"pullRequestReviewContributionsByRepository": []any{count(api, 7), count(lib, 3)},
				},
				"w0": map[string]any{"commitContributionsByRepository": []any{commits(api, 3, 4), commits(web, 2)}},
				"w1": map[string]any{"commitContributionsByRepository": []any{commits(api, 1)}},
				"w2": map[string]any{"commitContributionsByRepository": []any{}},
				"w3": map[string]any{"commitContributionsByRepository": []any{}},
				"w4": map[string]any{"commitContributionsByRepository": []any{}},
			}
			if hidden {
				viewer["all"] = map[string]any{"contributionCalendar": map[string]any{"totalContributions": 0, "weeks": []any{}}}
				for _, w := range []string{"w0", "w1"} {
					viewer[w] = map[string]any{"commitContributionsByRepository": []any{}}
				}
			}
			reply(w, map[string]any{"data": map[string]any{"viewer": viewer}})

		case strings.Contains(q, "reviews(first"):
			if vars["q"] != "is:pr reviewed-by:octo -author:octo created:<=2025-12-31 updated:>=2025-01-01" || vars["login"] != "octo" {
				t.Errorf("reviews vars %v", vars)
			}
			pr := func(author string, r map[string]any, reviews ...map[string]any) map[string]any {
				return map[string]any{"author": map[string]any{"__typename": author}, "reviews": map[string]any{"nodes": reviews}, "repository": r}
			}
			review := func(state, at string) map[string]any { return map[string]any{"state": state, "submittedAt": at} }
			const in = "2025-06-01T00:00:00Z"
			nodes := []any{
				pr("User", api, review("APPROVED", in), review("APPROVED", "2024-06-01T00:00:00Z"), map[string]any{"state": "PENDING"}),
				pr("Bot", api, review("APPROVED", in)),
				pr("User", lib, review("CHANGES_REQUESTED", in)),
			}
			next := map[string]any{"hasNextPage": true, "endCursor": "c1"}
			if vars["after"] == "c1" {
				nodes = []any{pr("User", lib, review("APPROVED", in))}
				if hidden {
					nodes = append(nodes, pr("User", secret, review("APPROVED", in)))
				}
				next = map[string]any{"hasNextPage": false}
			}
			reply(w, map[string]any{"data": map[string]any{"search": map[string]any{"issueCount": 4, "pageInfo": next, "nodes": nodes}}})

		case strings.Contains(q, "... on Issue"):
			var nodes []any
			switch vars["q"] {
			case "is:pr author:octo created:2025-01-01..2025-12-31":
				nodes = withRepo(api, 5)
			case "is:issue author:octo created:2025-01-01..2025-12-31":
				nodes = withRepo(web, 2)
			default:
				t.Errorf("search %q", vars["q"])
			}
			if hidden {
				nodes = append(nodes, withRepo(secret, 1)...)
			}
			reply(w, searchReply(nodes))

		case strings.Contains(q, "search(query: $q"):
			if vars["q"] != "is:pr is:merged author:octo merged:2025-01-01..2025-12-31" {
				t.Errorf("search %q", vars["q"])
			}
			pr := func(title string, add, del int, hours int) map[string]any {
				created := time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC)
				return map[string]any{"title": title, "url": "https://github.com/acme/api/pull/" + title, "additions": add, "deletions": del,
					"createdAt": created, "mergedAt": created.Add(time.Duration(hours) * time.Hour), "repository": api}
			}
			reply(w, map[string]any{"data": map[string]any{"search": map[string]any{"issueCount": 2, "pageInfo": map[string]any{"hasNextPage": false},
				"nodes": []any{pr("1", 100, 10, 2), pr("2", 5, 500, 10), map[string]any{}}}}})

		case strings.Contains(q, "vulnerabilityAlerts"):
			alert := func(at string, sev string) map[string]any {
				return map[string]any{"fixedAt": at, "securityVulnerability": map[string]any{"severity": sev}}
			}
			if vars["after"] == "v1" {
				reply(w, map[string]any{"data": map[string]any{"repository": map[string]any{"vulnerabilityAlerts": map[string]any{
					"pageInfo": map[string]any{"hasNextPage": false}, "nodes": []any{alert("2025-09-01T00:00:00Z", "MODERATE")}}}}})
				return
			}
			data, errs := map[string]any{}, []any{}
			for i := 0; vars["o"+itoa(i)] != nil; i++ {
				alias, name := "r"+itoa(i), vars["o"+itoa(i)].(string)+"/"+vars["n"+itoa(i)].(string)
				switch name {
				case "acme/api":
					data[alias] = map[string]any{"vulnerabilityAlerts": map[string]any{"pageInfo": map[string]any{"hasNextPage": true, "endCursor": "v1"},
						"nodes": []any{alert("2025-02-01T00:00:00Z", "HIGH"), alert("2025-03-01T00:00:00Z", "CRITICAL"), alert("2024-03-01T00:00:00Z", "LOW")}}}
				case "acme/web":
					data[alias] = nil
					errs = append(errs, map[string]any{"message": "no access", "path": []string{alias}})
				default:
					data[alias] = map[string]any{"vulnerabilityAlerts": map[string]any{"pageInfo": map[string]any{}, "nodes": []any{}}}
				}
			}
			reply(w, map[string]any{"data": data, "errors": errs})

		case strings.Contains(q, "r0: repository"):
			if vars["o0"] != "acme" || vars["n0"] != "secret" || vars["o1"] != nil {
				t.Errorf("language lookup vars %v", vars)
			}
			reply(w, map[string]any{"data": map[string]any{"r0": repo("acme/secret", "Rust", true)}})

		default:
			t.Errorf("unexpected query %s", q)
		}
	})
	mux.HandleFunc("GET /search/commits", func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query().Get("q"); q != "author:octo author-date:2025-01-01..2025-12-31" {
			t.Errorf("commit search %q", q)
		}
		item := func(name string, private bool) map[string]any {
			return map[string]any{"repository": map[string]any{"full_name": name, "private": private}}
		}
		var items []any
		for range 8 {
			items = append(items, item("acme/api", false))
		}
		items = append(items, item("acme/web", true), item("acme/web", true))
		if hidden {
			items = append(items, item("acme/secret", true), item("acme/secret", true), item("acme/secret", true))
		}
		reply(w, map[string]any{"total_count": len(items), "items": items})
	})
	mux.HandleFunc("GET /repos/{owner}/{repo}/code-scanning/alerts", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("owner")+"/"+r.PathValue("repo") != "acme/api" {
			http.Error(w, `{"message":"Advanced Security must be enabled"}`, http.StatusForbidden)
			return
		}
		reply(w, []any{
			map[string]any{"fixed_at": "2025-06-01T00:00:00Z", "updated_at": "2025-06-01T00:00:00Z"},
			map[string]any{"fixed_at": "2024-06-01T00:00:00Z", "updated_at": "2024-06-01T00:00:00Z"},
			map[string]any{"fixed_at": "2025-06-01T00:00:00Z", "updated_at": "2024-01-01T00:00:00Z"}, // after the stop
		})
	})
	mux.HandleFunc("GET /repos/{owner}/{repo}/secret-scanning/alerts", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("owner")+"/"+r.PathValue("repo") != "acme/web" {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		reply(w, []any{
			map[string]any{"resolved_at": "2025-07-01T00:00:00Z", "updated_at": "2025-07-01T00:00:00Z", "resolved_by": map[string]any{"login": "Octo"}},
			map[string]any{"resolved_at": "2025-07-01T00:00:00Z", "updated_at": "2025-07-01T00:00:00Z", "resolved_by": map[string]any{"login": "someone"}},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func itoa(i int) string { return string(rune('0' + i)) }

func TestWrapped(t *testing.T) {
	srv := fakeWrapped(t, false)
	gh := &github{base: srv.URL, token: "test-token", http: srv.Client()}
	from, to, err := yearRange(2025, time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	w, err := gh.wrapped(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	if w.Login != "octo" || w.Commits != 10 || w.PRsOpened != 5 || w.PRsMerged != 2 || w.Reviews != 10 || w.Issues != 2 ||
		w.ReposCreated != 1 || w.PrivateContributions != 4 || w.Contributions != 20 {
		t.Errorf("totals %+v", w)
	}
	if w.Approvals != 3 || w.BotPRsApproved != 1 || w.ChangesRequested != 1 {
		t.Errorf("reviews: %d approvals, %d bot, %d changes requested", w.Approvals, w.BotPRsApproved, w.ChangesRequested)
	}
	if w.Additions != 105 || w.Deletions != 510 || w.MedianMergeSeconds != 10*3600 || w.BiggestPR == nil || w.BiggestPR.Title != "2" {
		t.Errorf("merged: +%d -%d median %ds biggest %+v", w.Additions, w.Deletions, w.MedianMergeSeconds, w.BiggestPR)
	}
	sec := w.Security
	if sec.DependabotFixed != 3 || sec.BySeverity["critical"] != 1 || sec.BySeverity["high"] != 1 || sec.BySeverity["moderate"] != 1 ||
		sec.CodeScanningFixed != 1 || sec.SecretsResolved != 1 || sec.ReposChecked != 3 || sec.ReposUnavailable != 0 {
		t.Errorf("security %+v", sec)
	}
	c := w.Calendar
	if c.ActiveDays != 7 || c.LongestStreak != 2 || c.CurrentStreak != 2 || c.BusiestDay != "2025-03-04" || c.BusiestWeekday != "Tuesday" || c.Months[0] != 10 {
		t.Errorf("calendar %+v", c)
	}
	if len(w.Languages) != 2 || w.Languages[0] != (NameCount{"Go", 8}) || w.Languages[1] != (NameCount{"TypeScript", 2}) {
		t.Errorf("languages %+v", w.Languages)
	}
	want := map[string]RepoWrapped{
		"acme/api":  {Name: "acme/api", Language: "Go", Commits: 8, PRsOpened: 5, PRsMerged: 2, Reviews: 7, Approvals: 2, Additions: 105, Deletions: 510, DependabotFixed: 3, CodeScanningFixed: 1},
		"other/lib": {Name: "other/lib", Reviews: 3, Approvals: 1},
		"acme/web":  {Name: "acme/web", Private: true, Language: "TypeScript", Commits: 2, Issues: 2, SecretsResolved: 1},
	}
	if len(w.Repos) != 3 || w.Repos[0].Name != "acme/api" {
		t.Fatalf("repos %+v", w.Repos)
	}
	for _, r := range w.Repos {
		if r != want[r.Name] {
			t.Errorf("repo %+v\nwant %+v", r, want[r.Name])
		}
	}
	if len(w.Warnings) != 0 {
		t.Errorf("warnings %v", w.Warnings)
	}

	out := renderWrapped(w, 2, 100)
	for _, s := range []string{
		"GitHub Wrapped 2025", "@octo (Octo Cat)", "Jan 1 – Dec 31, 2025", "commits", "across 2 repos",
		"+105 −510 lines · median 10h to merge", "1 bot PR approved", "Dependabot 3 (1 critical, 1 high, 1 moderate)",
		"secrets you resolved 1", "4 private contributions", "Go 80% · TypeScript 20%", "Tuesdays", "The Gatekeeper",
		"Tue, Mar 4", "By repo (top 2 of 3; --top 0 for all)", "acme/api",
	} {
		if !strings.Contains(out, s) {
			t.Errorf("render missing %q:\n%s", s, out)
		}
	}
	if !strings.Contains(out, "acme/web 🔒") || strings.Contains(out, "other/lib") || !strings.Contains(renderWrapped(w, 0, 100), "other/lib") {
		t.Errorf("--top not applied:\n%s", out)
	}
}

// The contribution graph can omit private repos without saying so, so search
// must still find the activity in them.
func TestWrappedActivityHiddenFromGraph(t *testing.T) {
	srv := fakeWrapped(t, true)
	gh := &github{base: srv.URL, token: "test-token", http: srv.Client()}
	from, to, err := yearRange(2025, time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	w, err := gh.wrapped(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	if w.Commits != 13 || w.PRsOpened != 6 || w.Issues != 3 || w.Reviews != 5 || w.Approvals != 4 || w.BotPRsApproved != 1 || w.ChangesRequested != 1 {
		t.Errorf("totals %+v", w)
	}
	var got RepoWrapped
	for _, r := range w.Repos {
		if r.Name == "acme/secret" {
			got = r
		}
	}
	if want := (RepoWrapped{Name: "acme/secret", Private: true, Language: "Rust", Commits: 3, PRsOpened: 1, Reviews: 1, Approvals: 1, Issues: 1}); got != want {
		t.Errorf("repo %+v\nwant %+v", got, want)
	}
	if len(w.Warnings) != 1 || !strings.Contains(w.Warnings[0], "contribution graph") {
		t.Errorf("warnings %v", w.Warnings)
	}
}

func TestCalendarStats(t *testing.T) {
	days := []calendarDay{{"2026-10-30", 1}, {"2026-10-31", 2}, {"2026-11-01", 0}, {"2026-11-02", 3}, {"2026-11-03", 0}}
	// Today has no contributions yet, so yesterday's streak is still current.
	s := calendarStats(days, time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC))
	if s.ActiveDays != 3 || s.LongestStreak != 2 || s.CurrentStreak != 1 || s.BusiestDay != "2026-11-02" || s.BusiestWeekday != "Monday" {
		t.Errorf("%+v", s)
	}
	// Days after to are ignored; a gap in the graph breaks a streak.
	s = calendarStats([]calendarDay{{"2026-01-01", 1}, {"2026-01-03", 1}, {"2026-01-04", 5}}, time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC))
	if s.ActiveDays != 2 || s.LongestStreak != 1 || s.CurrentStreak != 1 {
		t.Errorf("%+v", s)
	}
}

func TestWrappedHelpers(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	if _, to, err := yearRange(2026, now); err != nil || !to.Equal(now) {
		t.Errorf("current year ends now: %v %v", to, err)
	}
	for _, y := range []int{2007, 2027} {
		if _, _, err := yearRange(y, now); err == nil {
			t.Errorf("year %d accepted", y)
		}
	}
	from := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	wins := splitWindows(from, from.AddDate(0, 0, 100), 90*24*time.Hour)
	if len(wins) != 2 || !wins[1][0].Equal(from.AddDate(0, 0, 90)) || !wins[0][1].Equal(wins[1][0].Add(-time.Second)) {
		t.Errorf("windows %v", wins)
	}
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567", -4500: "-4,500"} {
		if got := commas(n); got != want {
			t.Errorf("commas(%d) = %q", n, got)
		}
	}
	var out, errOut strings.Builder
	if err := run([]string{"wrapped", "--year", "1999"}, &out, &errOut); err == nil || !strings.Contains(err.Error(), "invalid --year") {
		t.Errorf("bad year: %v", err)
	}
	if err := run([]string{"wrapped", "--top", "-1"}, &out, &errOut); err == nil {
		t.Error("negative --top accepted")
	}
}
