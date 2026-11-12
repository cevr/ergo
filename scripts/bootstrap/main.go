// Command bootstrap builds ergo from a pinned seed, the only way to an ergo
// once the compiler is written in Ergo (v1 capstone, "Ergo builds Ergo").
//
//	go run ./scripts/bootstrap [-o DIR] [-stages N]
//	go run ./scripts/bootstrap verify-seed [-work DIR] [-fresh-cache=false]
//	go run ./scripts/bootstrap seed-lock [-seed NAME] [-source COMMIT] [-go RELEASE] [-platforms LIST] [-work DIR]
//	go run ./scripts/bootstrap capstone
//
// The default command takes the seed that toolchain/seed.lock names (stage 0)
// from ergo's cache, or rebuilds it from its source commit and refuses it
// unless its sha256 is the lock's. Then it builds stage 1 to stage N (default
// 2) from the tree, each with the stage before it as its compiler, into
// DIR/stage<i>/ergo (default <repo>/.bootstrap), and requires the last two
// stages to be byte-identical.
//
// verify-seed builds the lock's source twice for each platform of the lock,
// from two clean exports at different paths with a Go cache each, and
// requires both builds to equal the lock. seed-lock makes the same builds
// and prints the lock a seed bump commits. capstone prints the 15 rows of the
// capstone probe, owned by the stage that makes each hold; it exits 1 while
// any row is red.
//
// Every build is go build -trimpath -buildvcs=false with CGO_ENABLED=0 and an
// environment reduced to what locates programs and caches (buildEnv), so a
// build is a function of the source and the Go release. ERGO_GO selects that
// release as cmd/ergo does: unset or managed is the lock's pinned Go,
// fetched by the go command through GOTOOLCHAIN; system is the go on PATH.
//
// Exit codes: 0 done; 1 a build failed, a seed or stage did not reproduce, or
// a capstone row is red; 2 invalid invocation.
package main

import (
	"errors"
	"flag"
	"fmt"
	iofs "io/fs"
	"os"
	"path/filepath"
	"strings"

	"ergo.local/prototype/internal/sketch/build"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "bootstrap:", err)
		var u usageError
		if errors.As(err, &u) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

type usageError struct{ error }

func run(args []string) error {
	cmd := "build"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	repo, err := git(".", "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	if cmd == "capstone" {
		if len(args) > 0 {
			return usageError{fmt.Errorf("capstone takes no arguments")}
		}
		if red, _ := reportCapstone(os.Stdout, capstoneRows(repo)); red > 0 {
			return fmt.Errorf("%d capstone rows are red", red)
		}
		return nil
	}
	l, err := readLock(filepath.Join(repo, lockFile))
	if errors.Is(err, iofs.ErrNotExist) && cmd == "seed-lock" {
		// The first lock: name everything on the command line.
		l, err = seedLock{Seed: "seed-0", Package: "./cmd/ergo", Platforms: []string{"linux/amd64", "darwin/arm64"}}, nil
	}
	if err != nil {
		return err
	}
	cacheRoot, err := build.DefaultRoot()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	out := fs.String("o", filepath.Join(repo, ".bootstrap"), "the directory of the stages")
	n := fs.Int("stages", 2, "the number of stages to build after the seed")
	work := fs.String("work", "", "the scratch directory of the seed builds (default: a new temporary directory, removed after)")
	fresh := fs.Bool("fresh-cache", true, "give each seed build a Go cache of its own")
	seed := fs.String("seed", l.Seed, "seed-lock: the new seed's name, also its git tag")
	source := fs.String("source", "", "seed-lock: the commit the new seed is built from (default: the lock's)")
	goRelease := fs.String("go", l.Go, "seed-lock: the Go release that builds it")
	platforms := fs.String("platforms", strings.Join(l.Platforms, ","), "seed-lock: the platforms, comma-separated")
	if err := fs.Parse(args); err != nil {
		return usageError{err}
	}
	if fs.NArg() > 0 {
		return usageError{fmt.Errorf("unexpected arguments %q", fs.Args())}
	}
	if cmd == "seed-lock" {
		l.Seed, l.Go = *seed, *goRelease
		if *source != "" {
			if l.Source, err = git(repo, "rev-parse", "--verify", *source+"^{commit}"); err != nil {
				return err
			}
		}
		l.Platforms = strings.Split(*platforms, ",")
		if l.Source == "" || l.Go == "" {
			return usageError{fmt.Errorf("seed-lock with no %s needs -source and -go", lockFile)}
		}
	}
	tool, err := selectGo(os.Environ(), l.Go)
	if err != nil {
		return usageError{err}
	}
	d := driver{repo: repo, environ: os.Environ(), tool: tool, cacheRoot: cacheRoot}
	switch cmd {
	case "build":
		_, err := d.stages(l, *out, *n)
		return err
	case "verify-seed", "seed-lock":
		dir := *work
		if dir == "" {
			if dir, err = os.MkdirTemp("", "ergo-seed-"); err != nil {
				return err
			}
			defer os.RemoveAll(dir)
		}
		if cmd == "verify-seed" {
			if err := d.verifySeed(l, dir, *fresh); err != nil {
				return err
			}
			fmt.Printf("%s reproduces %s for %s\n", lockFile, l.Seed, strings.Join(l.Platforms, ", "))
			return nil
		}
		sums, err := d.reproduce(l, l.Platforms, dir, *fresh)
		if err != nil {
			return err
		}
		l.Sums = sums
		fmt.Print(l.format())
		return nil
	}
	return usageError{fmt.Errorf("unknown command %q: want build, verify-seed, seed-lock or capstone", cmd)}
}
