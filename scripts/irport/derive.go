package main

import (
	"fmt"
	"os"
	"path/filepath"

	"ergo.local/prototype/internal/toolchain"
)

// writePatch writes the patch the source tree src makes (toolchain.
// DerivePatch) to patch, and its digest into lock's line for it
// (`irport -write`, first in the regeneration order).
func writePatch(src, patch, lock string) error {
	data, err := toolchain.DerivePatch(src)
	if err != nil {
		return err
	}
	l, err := os.ReadFile(lock)
	if err != nil {
		return err
	}
	l, err = toolchain.LockDigest(l, filepath.Base(patch), data)
	if err != nil {
		return err
	}
	if err := os.WriteFile(patch, data, 0o644); err != nil {
		return err
	}
	return os.WriteFile(lock, l, 0o644)
}

// extractPatch writes the source tree of the patch file patch into dir,
// which must not exist (`irport -extract`): a branch's edited patch as
// files, each to merge with git merge-file against main's toolchain/ir/_go.
func extractPatch(patch, dir string) error {
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("%s exists", dir)
	}
	data, err := os.ReadFile(patch)
	if err != nil {
		return err
	}
	files, hooks, err := toolchain.ExtractPatch(data)
	if err != nil {
		return err
	}
	files[toolchain.GoFilesPatch] = hooks
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, content, 0o644); err != nil {
			return err
		}
	}
	return nil
}
