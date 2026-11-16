package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// driver is what every command of the bootstrap shares.
type driver struct {
	repo       string   // the repository root: the tree, and the git history of the seed's source
	environ    []string // the caller's environment
	tool       goTool
	cacheRoot  string       // ergo's cache root; seeds live in <cacheRoot>/seed
	client     *http.Client // seed downloads; nil is a client with a hang timeout
	fromSource bool         // never download a seed; rebuild it from its source
}

func hostPlatform() string { return runtime.GOOS + "/" + runtime.GOARCH }

// checkSource refuses a lock whose source is not a commit of the repository,
// and a tag of the seed's name that names another commit.
func (d driver) checkSource(l seedLock) error {
	if _, err := git(d.repo, "rev-parse", "--verify", "--quiet", l.Source+"^{commit}"); err != nil {
		return fmt.Errorf("the source of %s, %s, is not a commit of %s; fetch it (tag %s)", l.Seed, l.Source, d.repo, l.Seed)
	}
	tag, err := git(d.repo, "rev-parse", "--verify", "--quiet", "refs/tags/"+l.Seed+"^{commit}")
	if err == nil && tag != l.Source {
		return fmt.Errorf("tag %s names %s, but %s says its source is %s", l.Seed, tag, lockFile, l.Source)
	}
	return nil
}

// buildSeed exports the lock's source into src, a new directory, builds the
// seed for platform into out with Go's cache cache ("" for the caller's),
// and returns its sha256. The binary must name the lock's Go release.
func (d driver) buildSeed(l seedLock, platform, src, out, cache string) (string, error) {
	if err := d.checkSource(l); err != nil {
		return "", err
	}
	if err := os.MkdirAll(src, 0o755); err != nil {
		return "", err
	}
	if err := export(d.repo, l.Source, src); err != nil {
		return "", err
	}
	b := goBuild{environ: d.environ, tool: d.tool, dir: src, pkg: l.Package, platform: platform, out: out, cache: cache}
	if err := b.run(); err != nil {
		return "", err
	}
	if v, err := goVersion(out); err != nil {
		return "", err
	} else if v != l.Go {
		return "", fmt.Errorf("%s was built by %s, but %s pins %s (ERGO_GO=%s)", l.Seed, v, lockFile, l.Go, lookup(d.environ, "ERGO_GO"))
	}
	return fileSum(out)
}

// seedPath is where the seed for platform is cached.
func (d driver) seedPath(l seedLock, platform string) string {
	return filepath.Join(d.cacheRoot, "seed", l.Seed+"."+strings.ReplaceAll(platform, "/", "-"), "ergo")
}

// ensureSeed returns the cached seed for platform when its sha256 is the
// lock's. Otherwise it downloads the seed from the lock's url (a GitHub
// release asset) and keeps it only with the lock's sha256; a download whose
// digest differs is refused. When the lock has no url, the release is not
// published, the host cannot be reached or fromSource is set, it rebuilds the
// seed from its source commit, refuses it unless its sha256 is the lock's,
// and caches it.
func (d driver) ensureSeed(l seedLock, platform string) (string, error) {
	want, ok := l.Sums[platform]
	if !ok {
		return "", fmt.Errorf("%s has no %s seed for %s", lockFile, l.Seed, platform)
	}
	path := d.seedPath(l, platform)
	if sum, err := fileSum(path); err == nil && sum == want {
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	work, err := os.MkdirTemp(filepath.Dir(path), "tmp-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(work)
	built := filepath.Join(work, "ergo")
	if l.URL != "" && !d.fromSource {
		err := d.download(l, platform, want, built)
		if err == nil {
			if err := os.Rename(built, path); err != nil {
				return "", err
			}
			return path, nil
		}
		if !errors.Is(err, errNoRelease) {
			return "", err
		}
		fmt.Fprintf(os.Stderr, "bootstrap: %v; rebuilding %s from %s\n", err, l.Seed, l.Source)
		if err := os.Remove(built); err != nil && !os.IsNotExist(err) {
			return "", err
		}
	}
	sum, err := d.buildSeed(l, platform, filepath.Join(work, "src"), built, "")
	if err != nil {
		return "", err
	}
	if sum != want {
		return "", fmt.Errorf("%s rebuilt from %s with %s for %s has sha256 %s; %s says %s", l.Seed, l.Source, l.Go, platform, sum, lockFile, want)
	}
	if err := os.Rename(built, path); err != nil {
		return "", err
	}
	return path, nil
}

// reproduce builds the lock's source twice for each platform, from two
// clean exports at different paths, each with a Go cache of its own when
// fresh is set, and returns the sha256 per platform. The two builds must be
// byte-identical.
func (d driver) reproduce(l seedLock, platforms []string, work string, fresh bool) (map[string]string, error) {
	sums := map[string]string{}
	for _, p := range platforms {
		// A directory of its own, so a work directory that an earlier run
		// left behind is no clean export.
		base, err := os.MkdirTemp(work, strings.ReplaceAll(p, "/", "-")+"-")
		if err != nil {
			return nil, err
		}
		var got [2]string
		for i, sub := range []string{"a", filepath.Join("b", "again")} {
			dir := filepath.Join(base, sub)
			cache := ""
			if fresh {
				cache = filepath.Join(dir, "gocache")
			}
			sum, err := d.buildSeed(l, p, filepath.Join(dir, "src"), filepath.Join(dir, "ergo"), cache)
			if err != nil {
				return nil, err
			}
			got[i] = sum
		}
		if got[0] != got[1] {
			return nil, fmt.Errorf("%s for %s is not reproducible: two clean builds of %s have sha256 %s and %s", l.Seed, p, l.Source, got[0], got[1])
		}
		sums[p] = got[0]
	}
	return sums, nil
}

// verifySeed reproduces every platform of the lock and compares it with the
// lock's sha256.
func (d driver) verifySeed(l seedLock, work string, fresh bool) error {
	sums, err := d.reproduce(l, l.Platforms, work, fresh)
	if err != nil {
		return err
	}
	var bad []string
	for _, p := range l.Platforms {
		if sums[p] != l.Sums[p] {
			bad = append(bad, fmt.Sprintf("%s: built %s, lock %s", p, sums[p], l.Sums[p]))
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("%s does not reproduce %s:\n  %s", l.Seed, lockFile, strings.Join(bad, "\n  "))
	}
	return nil
}
