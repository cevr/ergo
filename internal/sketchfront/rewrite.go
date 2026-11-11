package sketchfront

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"

	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/lower"
)

// rewriteEvidence is the evidence for the rewrite laws of a run build
// (types.md, Laws 14; lower.RewriteChecks): the build checks the laws its
// lowering uses, with each site instance among the cases, by a generated
// Go check run. The run's output is cached under the cache root by the
// digest of the compiler, the generated test files and the laws it runs,
// which hold every input of its verdicts; the check's module is a test
// module of the emission cache. A program with no rewrite law checks
// nothing.
func rewriteEvidence(unit lower.Unit, req BuildRequest, input build.Input) (lower.RewriteEvidence, error) {
	return lower.RewriteChecks(unit, func(files []lower.File, laws []string) (string, error) {
		hash := sha256.New()
		fmt.Fprintf(hash, "%d:%s", len(input.Compiler), input.Compiler)
		for _, f := range files {
			fmt.Fprintf(hash, "%d:%s%d:", len(f.Path), f.Path, len(f.Data))
			hash.Write(f.Data)
		}
		for _, law := range laws {
			fmt.Fprintf(hash, "%d:%s", len(law), law)
		}
		key := hex.EncodeToString(hash.Sum(nil))
		dir := filepath.Join(req.Cache.Root, "rewrite-checks")
		path := filepath.Join(dir, key+".txt")
		if data, err := os.ReadFile(path); err == nil {
			return string(data), nil
		}
		test := input
		test.Mode = build.ModeTest
		test.Dependencies = append(slices.Clone(input.Dependencies), sourceDependency("rewrite check", map[string]string{"key": key}))
		module, err := req.Cache.Module(test, func() ([]build.File, error) {
			out := make([]build.File, len(files))
			for i, f := range files {
				out[i] = build.File{Path: f.Path, Data: f.Data}
			}
			return out, nil
		})
		if err != nil {
			return "", err
		}
		defer module.Release()
		tmp, err := os.MkdirTemp("", "ergo-rewrite-check-")
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(tmp)
		// The check runs here, so it is built for the host.
		host := req.Toolchain
		host.GOOS, host.GOARCH, host.Tags = "", "", nil
		exe := filepath.Join(tmp, "check")
		child := host.Command(module, exe)
		stderr := req.Stderr
		if stderr == nil {
			stderr = io.Discard
		}
		child.Stdout, child.Stderr = stderr, stderr
		if err := child.Run(); err != nil {
			return "", fmt.Errorf("the rewrite-law check build failed: %w", err)
		}
		var out bytes.Buffer
		for _, law := range laws {
			run := exec.Command(exe, "--seed", lower.RewriteCheckSeed, "--law", law)
			run.Stdout, run.Stderr = &out, io.Discard
			// A refuted law exits non-zero; its line is its verdict.
			_ = run.Run()
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		part, err := os.CreateTemp(dir, key+".*.part")
		if err != nil {
			return "", err
		}
		_, werr := part.Write(out.Bytes())
		cerr := part.Close()
		if werr == nil && cerr == nil {
			werr = os.Rename(part.Name(), path)
		}
		if werr != nil || cerr != nil {
			_ = os.Remove(part.Name())
		}
		return out.String(), nil
	})
}
