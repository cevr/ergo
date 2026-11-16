package lint

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// SelectedPack is a rule pack selected from disk: its validated manifest,
// the manifest's absolute path, and the directory holding it. The
// executable path resolves against Dir, and the pack runs there (it is the
// RunOptions.Dir of the pack).
type SelectedPack struct {
	Manifest Manifest
	Path     string
	Dir      string
}

// LoadManifest reads and validates the manifest file at path and admits
// its executable: resolved against the manifest's directory, never PATH,
// an existing executable must be a regular file. Admission inspects the
// file without opening or starting it. An absent executable loads, so
// rules can be inspected before a pack is built; the runner qualifies the
// executable again before each run, where absence is a spawn failure.
func LoadManifest(path string) (SelectedPack, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return SelectedPack{}, err
	}
	data, err := readBounded(absolute, MaxManifestBytes)
	if err != nil {
		return SelectedPack{}, fmt.Errorf("rule pack manifest %s: %w", path, err)
	}
	manifest, err := ParseManifest(data)
	if err != nil {
		return SelectedPack{}, fmt.Errorf("rule pack manifest %s: %w", path, err)
	}
	dir := filepath.Dir(absolute)
	if err := admitExecutable(manifest.Executable, dir); err != nil {
		return SelectedPack{}, fmt.Errorf("rule pack manifest %s: %w", path, err)
	}
	return SelectedPack{Manifest: manifest, Path: absolute, Dir: dir}, nil
}

// readBounded reads a regular file of at most limit bytes.
func readBounded(path string, limit int) ([]byte, error) {
	file, err := openRegularFile(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("exceeds %d bytes", limit)
	}
	return data, nil
}

// errNotRegular refuses a path that names a directory, FIFO, device or
// socket.
var errNotRegular = errors.New("not a regular file")

// openRegularFile admits a regular file before opening it, then checks the
// opened descriptor again: the file-admission policy of the CLI's source
// reader (internal/source), which this public SDK does not import. A FIFO
// or device is refused without being opened, so admission never waits for
// a writer.
func openRegularFile(path string) (*os.File, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errNotRegular
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if info, err := file.Stat(); err != nil || !info.Mode().IsRegular() {
		file.Close()
		if err != nil {
			return nil, err
		}
		return nil, errNotRegular
	}
	return file, nil
}
