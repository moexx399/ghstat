package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Each tmux badge is served from a cache so the status bar never waits on
// GitHub: a fresh cache is printed as is; a stale one is printed and a
// detached `ghstat badge KIND --refresh` rewrites it for the next tick.
type badgeCache struct {
	path string
	ttl  time.Duration
}

// refreshTimeout bounds how long a refresh may hold the lock before another
// may start, in case one is killed mid-way.
const refreshTimeout = 2 * time.Minute

// newBadgeCache is the cache for one badge; name tells badges (and their
// settings) apart, e.g. "ci" or "prs-7d".
func newBadgeCache(name string, ttl time.Duration) (badgeCache, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return badgeCache{}, err
	}
	return badgeCache{path: filepath.Join(dir, "ghstat", "badge-"+name), ttl: ttl}, nil
}

// read returns the cached badge and whether it is still fresh.
func (c badgeCache) read(now time.Time) (string, bool) {
	info, err := os.Stat(c.path)
	if err != nil {
		return "", false
	}
	b, err := os.ReadFile(c.path)
	if err != nil {
		return "", false
	}
	return string(b), now.Sub(info.ModTime()) < c.ttl
}

// write replaces the cached badge atomically.
func (c badgeCache) write(badge string) error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(c.path), "badge-*")
	if err != nil {
		return err
	}
	if _, err := tmp.WriteString(badge); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), c.path)
}

// lock claims the right to refresh, so overlapping status-bar ticks or tmux
// clients start one refresh rather than many. It reports false if another
// refresh holds it.
func (c badgeCache) lock(now time.Time) (unlock func(), ok bool) {
	lock := c.path + ".lock"
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		return nil, false
	}
	if info, err := os.Stat(lock); err == nil && now.Sub(info.ModTime()) > refreshTimeout {
		os.Remove(lock) // left behind by a killed refresh
	}
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, false
	}
	f.Close()
	return func() { os.Remove(lock) }, true
}

// startRefresh runs ghstat with args in the background, detached from this
// process's output so tmux need not wait.
func startRefresh(args []string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(self, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
