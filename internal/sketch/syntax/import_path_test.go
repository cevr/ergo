package syntax

import (
	"errors"
	"strings"
	"testing"
)

// The grammar requires nonempty path elements starting with a letter or
// underscore. A following comment does not make an invalid element valid.
// A Go import's path is one word to the parser: the checker applies Go's
// rule (module.CheckImportPath, #231), under which 0, h~ttp and h+ttp are
// paths (check's TestImportGoUses holds the malformed ones).
func TestImportPathGrammar(t *testing.T) {
	for _, form := range []struct{ prefix, suffix string }{
		{"import go ", "\n"},
		{"import go alias ", "\n"},
	} {
		for _, path := range []string{"0", "0//0", "net/http/", "net/.http", "net/h~ttp", "net/h+ttp", "github.com/0xAX/x"} {
			if _, err := Parse(form.prefix + path + form.suffix); err != nil {
				t.Errorf("%q: %v; a Go import path is the checker's (Go's rule)", form.prefix+path, err)
			}
		}
	}
	for _, form := range []struct{ prefix, suffix string }{
		{"import ", "\n"},
		{"import alias ", "\n"},
		{"extern go ", ".F func F()\n"},
	} {
		for _, path := range []string{"0", "0//0", "net/http/", "net/.http", "net/h~ttp", "net/h+ttp"} {
			src := form.prefix + path + form.suffix
			_, err := Parse(src)
			var e *Error
			if !errors.As(err, &e) || e.Code != CodeSyntax || !strings.Contains(e.Message, "path") {
				t.Errorf("%q: got %v, want S001 for the malformed path", src, err)
				continue
			}
			if path == "0//0" && e.Offset != len(form.prefix) {
				t.Errorf("%q: S001 at byte %d, want the leading digit at %d", src, e.Offset, len(form.prefix))
			}
		}
		for _, path := range []string{"strings", "net/http", "example.com/json", "gopkg.in/yaml.v3", "_a/pkg-name", "α/β2"} {
			src := form.prefix + path + form.suffix
			if _, err := Parse(src); err != nil {
				t.Errorf("%q: %v", src, err)
			}
		}
	}
}

// Comments have the same boundary after a path as after any other token.
func TestImportPathCommentBoundaries(t *testing.T) {
	for _, c := range []struct{ src, path, comment string }{
		{"import strings// note", "strings", "// note"},
		{"import strings/* c */", "strings", "/* c */"},
		{"import strings /* c */", "strings", "/* c */"},
		{"import a//b", "a", "//b"},
		{"import net//http", "net", "//http"},
		{"import a/*note*/", "a", "/*note*/"},
		{"import go strings// note", "strings", "// note"},
		{"import alias net/http// note", "net/http", "// note"},
		{"import go alias net/http/* note */", "net/http", "/* note */"},
		{"extern go fixture.mark/* note */ func Mark()", "fixture.mark", "/* note */"},
	} {
		f, err := Parse(c.src)
		if err != nil {
			t.Errorf("%q: %v", c.src, err)
			continue
		}
		var path string
		switch d := f.Decls[0].(type) {
		case *ImportDecl:
			path = d.Specs[0].Path.Value
		case *FuncDecl:
			path = d.Extern.Target.Value
		}
		if path != c.path {
			t.Errorf("%q: path %q, want %q", c.src, path, c.path)
		}
		if len(f.Comments) != 1 || len(f.Comments[0].List) != 1 || f.Comments[0].List[0].Text != c.comment {
			t.Errorf("%q: did not keep the separate comment %q", c.src, c.comment)
		}
	}
}
