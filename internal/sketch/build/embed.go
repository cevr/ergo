package build

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// The direct path embeds files as cmd/go does. resolveEmbed is
// cmd/go/internal/load.resolveEmbed (go1.27) for a package whose directory
// is on the local file system: each pattern is matched on every build, with
// Go's rules, and the compile key holds every pattern with the name and
// content digest of each file it embeds (embedKey), so adding, removing,
// renaming or editing a matching file compiles the package again.
//
// Where Go's rules end in an error (no match, an invalid name, a file in
// another module, an irregular file, a directory with nothing to embed),
// the direct path refuses and go build reports the error. A name whose
// validity depends on Unicode tables (any non-ASCII byte) is not decided
// here: it refuses too. The embedfollowsymlinks GODEBUG needs no case: it
// lets go build embed a matched symbolic link to a regular file, which
// this resolution calls irregular and refuses, and a walk skips symbolic
// links under either setting (cmd/go's resolveEmbed).

// errEmbedUndecided marks what the direct path leaves to go build without
// claiming it is an error.
var errEmbedUndecided = errors.New("left to go build")

// resolveEmbed maps each pattern to the sorted files, relative to pkgdir
// with slashes, that it embeds.
func resolveEmbed(pkgdir string, patterns []string) (map[string][]string, error) {
	pmap := map[string][]string{}
	dirOK := map[string]bool{}
	for _, pattern := range patterns {
		glob, all := strings.CutPrefix(pattern, "all:")
		if _, err := path.Match(glob, ""); err != nil || glob == "." || !fs.ValidPath(glob) {
			return nil, fmt.Errorf("pattern %s: invalid pattern syntax", pattern)
		}
		matches, err := filepath.Glob(quoteGlob(pkgdir) + string(filepath.Separator) + filepath.FromSlash(glob))
		if err != nil {
			return nil, fmt.Errorf("pattern %s: %v", pattern, err)
		}
		seen := map[string]bool{}
		var list []string
		add := func(rel string) {
			if !seen[rel] {
				seen[rel] = true
				list = append(list, rel)
			}
		}
		for _, file := range matches {
			rel := filepath.ToSlash(strings.TrimPrefix(file, pkgdir+string(filepath.Separator)))
			info, err := os.Lstat(file)
			if err != nil {
				return nil, fmt.Errorf("pattern %s: %v", pattern, err)
			}
			// No directory from the match up to the package begins
			// another module, is not a directory, or has a bad name.
			for dir := file; len(dir) > len(pkgdir)+1 && !dirOK[dir]; dir = filepath.Dir(dir) {
				if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
					return nil, fmt.Errorf("pattern %s: cannot embed %s: in different module", pattern, rel)
				}
				if dir != file {
					if info, err := os.Lstat(dir); err == nil && !info.IsDir() {
						return nil, fmt.Errorf("pattern %s: cannot embed %s: in non-directory", pattern, rel)
					}
				}
				bad, err := badEmbedName(filepath.Base(dir))
				if err != nil {
					return nil, fmt.Errorf("pattern %s: %w", pattern, err)
				}
				if bad {
					return nil, fmt.Errorf("pattern %s: cannot embed %s: invalid name", pattern, rel)
				}
				dirOK[dir] = true
			}
			switch {
			case info.Mode().IsRegular():
				add(rel)
			case info.IsDir():
				count := 0
				err := filepath.WalkDir(file, func(p string, d fs.DirEntry, err error) error {
					if err != nil {
						return err
					}
					rel := filepath.ToSlash(strings.TrimPrefix(p, pkgdir+string(filepath.Separator)))
					name := d.Name()
					if p != file {
						bad, err := badEmbedName(name)
						if err != nil {
							return err
						}
						hidden := name[0] == '.' || name[0] == '_'
						if bad || (hidden && !all) {
							switch {
							case d.IsDir():
								return fs.SkipDir
							case hidden:
								return nil
							default:
								return fmt.Errorf("cannot embed file %s: invalid name", rel)
							}
						}
					}
					if d.IsDir() {
						if _, err := os.Stat(filepath.Join(p, "go.mod")); err == nil {
							return fs.SkipDir
						}
						return nil
					}
					if !d.Type().IsRegular() {
						return nil
					}
					count++
					add(rel)
					return nil
				})
				if err != nil {
					return nil, fmt.Errorf("pattern %s: %w", pattern, err)
				}
				if count == 0 {
					return nil, fmt.Errorf("pattern %s: cannot embed directory %s: contains no embeddable files", pattern, rel)
				}
			default:
				return nil, fmt.Errorf("pattern %s: cannot embed irregular file %s", pattern, rel)
			}
		}
		if len(list) == 0 {
			return nil, fmt.Errorf("pattern %s: no matching files found", pattern)
		}
		slices.Sort(list)
		pmap[pattern] = list
	}
	return pmap, nil
}

// quoteGlob escapes the glob characters of a directory, as cmd/go's
// str.QuoteGlob does, so the package directory matches only itself.
func quoteGlob(dir string) string {
	var b strings.Builder
	for _, r := range dir {
		if strings.ContainsRune(`*?[\`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// windowsReserved are the names golang.org/x/mod/module rejects as a path
// element's part before its first dot, in any case.
var windowsReserved = []string{"CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9"}

// badEmbedName is cmd/go's isBadEmbedName: a name module.CheckFilePath
// rejects, or a version control directory. A non-ASCII name is not
// decided (module.CheckFilePath reads Unicode's letter table).
func badEmbedName(name string) (bool, error) {
	switch name {
	case ".bzr", ".hg", ".git", ".svn":
		return true, nil
	}
	// A name here is a file's base name, never empty. CheckFilePath's
	// "all dots" rule needs no case: such a name ends in a dot.
	if name[len(name)-1] == '.' {
		return true, nil
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 0x80:
			return false, fmt.Errorf("name %q is not ASCII: %w", name, errEmbedUndecided)
		case '0' <= c && c <= '9', 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', strings.IndexByte("!#$%&()+,-.=@[]^_{}~ ", c) >= 0:
		default:
			return true, nil
		}
	}
	short, _, _ := strings.Cut(name, ".")
	for _, reserved := range windowsReserved {
		if strings.EqualFold(reserved, short) {
			return true, nil
		}
	}
	return false, nil
}

// embedKey adds a package's embedded files to its compile key: the name
// and content digest of each file each pattern embeds. The pattern text is
// no input: two patterns that embed the same files compile alike, and the
// directive's text is in the source file's content already.
func embedKey(k *keyer, p *goPackage) error {
	for _, pattern := range slices.Sorted(mapKeys(p.Embed)) {
		for _, rel := range p.Embed[pattern] {
			data, err := os.ReadFile(filepath.Join(p.Dir, filepath.FromSlash(rel)))
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			k.add("embedded", rel)
			k.add("embedded content", hex.EncodeToString(sum[:]))
		}
	}
	return nil
}

// embedConfig is the -embedcfg file cmd/go writes (buildEmbedcfg): the
// files of each pattern, and where each file is.
func embedConfig(p *goPackage) ([]byte, error) {
	var cfg struct {
		Patterns map[string][]string
		Files    map[string]string
	}
	cfg.Patterns, cfg.Files = map[string][]string{}, map[string]string{}
	for pattern, list := range p.Embed {
		cfg.Patterns[pattern] = list
		for _, rel := range list {
			cfg.Files[rel] = filepath.Join(p.Dir, filepath.FromSlash(rel))
		}
	}
	return json.MarshalIndent(&cfg, "", "\t")
}
