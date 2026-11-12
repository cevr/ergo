package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// stages builds stage 1 to stage n of ergo from the tree into out/stage<i>/ergo:
// stage i is built with stage i-1 as its compiler, and stage 0 is the seed.
// The last two stages must be byte-identical.
//
// Until S1, the tree has no Ergo compiler package, so a stage's compiler
// lowers nothing and every stage is the pinned Go's build of the tree; the
// seed is still checked and run, so from the first Ergo package on the chain
// is the one that S6's fixpoint closes.
func (d driver) stages(l seedLock, out string, n int) ([]string, error) {
	if n < 2 {
		return nil, fmt.Errorf("-stages %d: want at least 2", n)
	}
	platform := hostPlatform()
	compiler, err := d.ensureSeed(l, platform)
	if err != nil {
		return nil, err
	}
	fmt.Printf("stage 0: %s %s (%s from %s)\n", l.Sums[platform], compiler, l.Seed, l.Source)
	var sums []string
	for i := 1; i <= n; i++ {
		if err := d.lower(compiler); err != nil {
			return nil, err
		}
		dst := filepath.Join(out, fmt.Sprintf("stage%d", i), "ergo")
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, err
		}
		b := goBuild{environ: d.environ, tool: d.tool, dir: d.repo, pkg: l.Package, platform: platform, out: dst}
		if err := b.run(); err != nil {
			return nil, err
		}
		sum, err := fileSum(dst)
		if err != nil {
			return nil, err
		}
		fmt.Printf("stage %d: %s %s\n", i, sum, dst)
		sums = append(sums, sum)
		compiler = dst
	}
	if sums[n-1] != sums[n-2] {
		return sums, fmt.Errorf("stage %d (%s) and stage %d (%s) differ: the build is not a fixpoint", n-1, sums[n-2], n, sums[n-1])
	}
	return sums, nil
}

// lower runs the stage's compiler over the tree's Ergo compiler packages,
// which go build then links with the Go packages. No compiler package exists
// before S1, so it only checks that the compiler runs on this host; a tree
// that has compiler/ is refused, not built without it.
func (d driver) lower(compiler string) error {
	cmd := exec.Command(compiler, "help")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("the stage compiler %s does not run on this host: %v", compiler, err)
	}
	if _, err := os.Stat(filepath.Join(d.repo, "compiler")); err == nil {
		return fmt.Errorf("%s holds Ergo compiler packages, and this driver cannot lower them yet: the mixed-binary step (the stage compiler lowers compiler/, Go links it) lands with S1", filepath.Join(d.repo, "compiler"))
	}
	return nil
}
