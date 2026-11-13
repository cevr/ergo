package build

import (
	"os"
	"path/filepath"
	"time"
)

// Warm manifests (compile-speed plan CS2): one file per request key under
// Root/warm, written whole by a rename. sketchfront's warm answer writes
// and reads them; the trim removes a manifest no request used for
// unusedLimit, as it removes the direct path's files.

// WarmManifest is the manifest of the request key, marked used, or false.
func (c Cache) WarmManifest(key string) ([]byte, bool) {
	path := filepath.Join(c.Root, "warm", key)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	if info, err := os.Stat(path); err == nil {
		mark(path, info, time.Now())
	}
	return data, true
}

// PutWarmManifest stores the manifest of the request key.
func (c Cache) PutWarmManifest(key string, data []byte) error {
	dir := filepath.Join(c.Root, "warm")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, key), data, dir)
}
