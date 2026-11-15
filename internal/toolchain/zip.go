package toolchain

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// hashZip is the h1 hash of a module zip (golang.org/x/mod/sumdb/dirhash
// Hash1): SHA-256 over the lines "SHA256(file)  NAME\n" of its files in
// the order of their names, in base64 after "h1:".
func hashZip(z *zip.Reader) (string, error) {
	files := slices.Clone(z.File)
	slices.SortStableFunc(files, func(a, b *zip.File) int { return strings.Compare(a.Name, b.Name) })
	var lines []string
	for _, f := range files {
		if strings.Contains(f.Name, "\n") {
			return "", fmt.Errorf("zip entry %q: a name with a newline", f.Name)
		}
		r, err := f.Open()
		if err != nil {
			return "", err
		}
		h := sha256.New()
		_, err = io.Copy(h, r)
		r.Close()
		if err != nil {
			return "", fmt.Errorf("zip entry %s: %w", f.Name, err)
		}
		lines = append(lines, fmt.Sprintf("%x  %s\n", h.Sum(nil), f.Name))
	}
	summary := sha256.New()
	for _, line := range lines {
		io.WriteString(summary, line)
	}
	return "h1:" + base64.StdEncoding.EncodeToString(summary.Sum(nil)), nil
}

// extract writes the files of the module zip under prefix into dir. Every
// name must start with prefix and stay inside dir. The zip holds no file
// modes, so, as cmd/go does for a toolchain, the commands in bin/ and
// pkg/tool/ become executable.
func extract(z *zip.Reader, prefix, dir string) error {
	seen := map[string]bool{}
	for _, f := range z.File {
		rel, ok := strings.CutPrefix(f.Name, prefix)
		if !ok || !filepath.IsLocal(rel) || strings.ContainsAny(rel, `\:`) || seen[rel] {
			return fmt.Errorf("zip entry %q: not a file of %s", f.Name, strings.TrimSuffix(prefix, "/"))
		}
		seen[rel] = true
		if strings.HasSuffix(rel, "/") {
			continue
		}
		mode := os.FileMode(0o644)
		if strings.HasPrefix(rel, "bin/") || strings.HasPrefix(rel, "pkg/tool/") {
			mode = 0o755
		}
		target := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := writeEntry(f, target, mode); err != nil {
			return err
		}
	}
	return nil
}

func writeEntry(f *zip.File, target string, mode os.FileMode) error {
	r, err := f.Open()
	if err != nil {
		return err
	}
	defer r.Close()
	w, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, r); err != nil {
		w.Close()
		return fmt.Errorf("zip entry %s: %w", f.Name, err)
	}
	return w.Close()
}
