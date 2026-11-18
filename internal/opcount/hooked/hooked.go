// Package hooked is the measured build of an emitted module for
// internal/opcount. It is a package of its own because it builds through
// the compiler front (sketchfront): opcount's counting needs none of it,
// so a checker edit does not reach opcount's tests (the fast tier's reach,
// scripts/gate TestFastTierReachOfAnEdit).
package hooked

import (
	"io"
	"os"
	"os/exec"
	"runtime"
	"slices"

	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/internal/toolchain"
)

// Build is the measured build of the module ergo emitted at work:
// ergo's own Go build action (build.Toolchain.Command, with the release
// ergo selects and its patched compile, and -ergoir for each package with
// an IR file), linking to bin, with the hook's link flag and overlay (a
// go build -overlay file that adds opcount's Hook as HookFile) added.
// Every Ergo body lowers through IR, which only the patched compile reads:
// a plain go build of the module stops at the first body-less IR
// declaration ("missing function body").
func Build(work, overlay, bin string) (*exec.Cmd, error) {
	root, err := build.DefaultRoot()
	if err != nil {
		return nil, err
	}
	selected, err := toolchain.Select(os.Getenv, root, io.Discard)
	if err != nil {
		return nil, err
	}
	pins, err := toolchain.PinSource(root, os.Getenv("ERGO_PIN_URL"), io.Discard)
	if err != nil {
		return nil, err
	}
	patched, err := sketchfront.PinnedRelease(selected, root, &pins, io.Discard)
	if err != nil {
		return nil, err
	}
	tc := build.HostToolchain()
	tc.Go, tc.Root = patched.Go, patched.Root
	tc.GOOS, tc.GOARCH = runtime.GOOS, runtime.GOARCH
	cmd := tc.Command(build.Module{Dir: work}, bin)
	// The pattern "." stays last: the hook's flags go before it.
	last := len(cmd.Args) - 1
	cmd.Args = append(append(slices.Clone(cmd.Args[:last]), "-ldflags=-checklinkname=0", "-overlay", overlay), cmd.Args[last])
	return cmd, nil
}
