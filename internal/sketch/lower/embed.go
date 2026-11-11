package lower

import (
	"path"
	"slices"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// packageEmbed uses the host's native embed directive, not a Go payload
// literal. Bytes remain subject to the ordinary collection value rule.
func (fc *funcCtx) packageEmbed(c *check.Call, name, typ string, site syntax.Node) {
	l := fc.l
	e := c.Embed
	if e == nil || len(e.Files) == 0 {
		ice(site, "embed has no checked asset snapshot")
	}
	l.embedUsed = true
	l.user.WriteString(l.directive(site))
	if e.Tree {
		if l.embedName == "" {
			l.embedName = l.pkgName(pkgKey{"import", "embed"}, "embed")
		}
		raw := l.pkgName(pkgKey{"embed", e.Prefix}, name+"Files")
		var patterns []string
		for _, f := range e.Files {
			patterns = append(patterns, strconv.Quote(path.Join(e.Prefix, f.Path)))
		}
		l.user.WriteString("//go:embed " + strings.Join(patterns, " ") + "\nvar " + raw + " " + l.embedName + ".FS\n")
		body, err := v1.BodyOf("EmbeddedFS")
		if err != nil {
			ice(site, "the embedded-tree constructor has no runtime body: %v", err)
		}
		call := fc.runtimeCall("EmbeddedFS", body, "", "", []string{raw, strconv.Quote(e.Prefix)}, "", nil, nil, site)
		l.user.WriteString("var " + name + " " + typ + " = " + call + "\n\n")
		return
	}
	l.user.WriteString("//go:embed " + strconv.Quote(path.Join(e.Prefix, e.Files[0].Path)) + "\nvar " + name + " " + typ + "\n\n")
}

// assetFiles returns deterministic build inputs from the checked facts,
// including imported packages. It never reads the file system in lowering.
func (l *lowerer) assetFiles() []File {
	byPath := map[string][]byte{}
	for _, s := range l.srcs {
		for _, c := range s.pkg.Info.Calls {
			if c.Embed == nil {
				continue
			}
			for _, f := range c.Embed.Files {
				byPath[path.Join(c.Embed.Prefix, f.Path)] = f.Data
			}
		}
	}
	names := make([]string, 0, len(byPath))
	for name := range byPath {
		names = append(names, name)
	}
	slices.Sort(names)
	var out []File
	for _, name := range names {
		out = append(out, File{Path: name, Data: slices.Clone(byPath[name])})
	}
	return out
}
