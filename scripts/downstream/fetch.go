package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// cacheVariable moves the downstream cache. By default it is
// downstream/ under the user's ergo cache ($XDG_CACHE_HOME/ergo, else the
// user's cache directory's ergo): not ERGO_CACHE, which a gate step moves
// to a scratch folder of its own, while the integrator's fetch must reach
// every later gate.
const cacheVariable = "ERGO_DOWNSTREAM_CACHE"

// defaultCache is the downstream cache's folder.
func defaultCache() (string, error) {
	if dir := os.Getenv(cacheVariable); dir != "" {
		if !filepath.IsAbs(dir) {
			return "", fmt.Errorf("%s=%q is not absolute", cacheVariable, dir)
		}
		return dir, nil
	}
	user, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(user, "ergo", "downstream"), nil
}

// entry is the cache folder of p: its pinned commit's tree, with no .git.
func entry(cache string, p pin) string {
	return filepath.Join(cache, p.name+"@"+p.commit)
}

// gitRuns counts the git processes fetch started: a pin already in the
// cache costs none (TestFetchTakesTheCommit).
var gitRuns int

// git runs git in dir and returns its stdout; the error holds its stderr.
func git(dir string, args ...string) ([]byte, error) {
	gitRuns++
	command := exec.Command("git", append([]string{"--no-optional-locks"}, args...)...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	out, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %v\n%s", strings.Join(args, " "), err, stderr.Bytes())
	}
	return out, nil
}

// fetch brings each pin's commit into the cache: a clone with no blobs
// until checkout, checked out detached at the commit, its .git removed,
// then renamed into place, so an entry is whole or absent. A pin whose
// entry exists does no Git work. It returns 1 when a pin could not be
// fetched, else 0. It is the only command that reads the network.
func fetch(out io.Writer, cache string, pins []pin) int {
	if err := os.MkdirAll(cache, 0o755); err != nil {
		fmt.Fprintf(out, "FAIL downstream: %v\n", err)
		return 1
	}
	failed := 0
	for _, p := range pins {
		dest := entry(cache, p)
		if info, err := os.Stat(dest); err == nil && info.IsDir() {
			fmt.Fprintf(out, "cached %s %s\n", p.name, p.commit)
			continue
		}
		if err := fetchOne(cache, dest, p); err != nil {
			fmt.Fprintf(out, "FAIL %s: fetch %s at %s: %v\n", p.name, p.url, p.commit, err)
			failed++
			continue
		}
		fmt.Fprintf(out, "fetched %s %s\n", p.name, p.commit)
	}
	if failed > 0 {
		return 1
	}
	return 0
}

// fetchOne clones p into a temporary folder of cache and renames it to
// dest once it holds exactly p's commit.
func fetchOne(cache, dest string, p pin) error {
	tmp, err := os.MkdirTemp(cache, ".fetch-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	tree := filepath.Join(tmp, "tree")
	if _, err := git(tmp, "clone", "--quiet", "--filter=blob:none", "--no-checkout", p.url, tree); err != nil {
		return err
	}
	if _, err := git(tree, "-c", "advice.detachedHead=false", "checkout", "--quiet", "--detach", p.commit); err != nil {
		return err
	}
	head, err := git(tree, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if got := strings.TrimSpace(string(head)); got != p.commit {
		return fmt.Errorf("checked out %s, not the pinned commit", got)
	}
	if err := os.RemoveAll(filepath.Join(tree, ".git")); err != nil {
		return err
	}
	return os.Rename(tree, dest)
}
