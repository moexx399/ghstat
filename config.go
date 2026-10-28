package main

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Repo is one line of the repos file: "owner/repo" or "owner/repo:branch".
// An empty Branch means the repository's default branch.
type Repo struct {
	Name, Branch string
}

// defaultConfigPath is $GHSTAT_CONFIG, else ghstat/repos.conf in the
// user config dir, falling back to the ghws script's ~/.config/ghws/repos.conf
// when only that exists.
func defaultConfigPath() string {
	if p := os.Getenv("GHSTAT_CONFIG"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "repos.conf"
	}
	path := filepath.Join(dir, "ghstat", "repos.conf")
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		if home, err := os.UserHomeDir(); err == nil {
			legacy := filepath.Join(home, ".config", "ghws", "repos.conf")
			if _, err := os.Stat(legacy); err == nil {
				return legacy
			}
		}
	}
	return path
}

// loadRepos reads the repos file; blank lines and # comments are ignored.
// branch, if set, overrides every repo's branch.
func loadRepos(path, branch string) ([]Repo, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("no repos file at %s; create it with one owner/repo[:branch] per line", path)
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var repos []Repo
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line, _, _ := strings.Cut(sc.Text(), "#")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, b, _ := strings.Cut(line, ":")
		if owner, repo, ok := strings.Cut(name, "/"); !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
			return nil, fmt.Errorf("%s:%d: want owner/repo[:branch], got %q", path, n, line)
		}
		if branch != "" {
			b = branch
		}
		repos = append(repos, Repo{Name: name, Branch: b})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(repos) == 0 {
		return nil, fmt.Errorf("no repos in %s", path)
	}
	return repos, nil
}
