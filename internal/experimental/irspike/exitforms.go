package irspike

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"

	"ergo.local/prototype/internal/sketch/lower"
)

// ExitForms counts, over the example binaries and the corpus programs of
// the repository at root, the IR bodies whose exit actions take each form
// (FLIP F7 STATIC-EXIT2): "scope" (the general frame scope, OnExit),
// "slots" (v1.Exits, ExitsOnExit), "defer" (a Go defer of Exits.Run) and
// "quiet" (of Exits.Quiet). A body is counted once per form it holds, a
// body of several programs once (by its IR name), and std bodies with the
// programs that lower them. A program that does not load or lower is
// skipped and counted in the "unbuilt" row.
func ExitForms(root string, progress io.Writer) (map[string][]string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	mains, _, err := ExampleTargets(root)
	if err != nil {
		return nil, err
	}
	files, err := corpusFiles(root)
	if err != nil {
		return nil, err
	}
	seen := map[string]map[string]bool{}
	unbuilt := 0
	for _, path := range append(mains, files...) {
		u, err := Load(path)
		if err != nil {
			unbuilt++
			fmt.Fprintf(progress, "unbuilt %s: %v\n", path, err)
			continue
		}
		out, _, err := lower.LowerIR(u)
		if err != nil {
			unbuilt++
			fmt.Fprintf(progress, "unbuilt %s: %v\n", path, err)
			continue
		}
		for _, f := range out {
			if filepath.Base(f.Path) != lower.IRFileName { // one per package when the build splits
				continue
			}
			var ir struct {
				Funcs []struct {
					Name string `json:"name"`
					Body any    `json:"body"`
				} `json:"funcs"`
			}
			if err := json.Unmarshal(f.Data, &ir); err != nil {
				return nil, err
			}
			for _, fn := range ir.Funcs {
				kinds := map[string]bool{}
				callKinds(fn.Body, kinds)
				for form, k := range map[string]string{"scope": "pcall OnExit", "slots": "pcall ExitsOnExit", "defer": "defer Run", "quiet": "defer Quiet"} {
					if kinds[k] {
						if seen[form] == nil {
							seen[form] = map[string]bool{}
						}
						seen[form][fn.Name] = true
					}
				}
			}
		}
		fmt.Fprintf(progress, "exitforms %s\n", path)
	}
	forms := map[string][]string{"unbuilt": {fmt.Sprint(unbuilt)}}
	for form, names := range seen {
		for n := range names {
			forms[form] = append(forms[form], n)
		}
		sort.Strings(forms[form])
	}
	return forms, nil
}

// callKinds adds to into "pcall NAME" for each package call under v, the
// IR's JSON, and "defer NAME" for each deferred method call.
func callKinds(v any, into map[string]bool) {
	switch x := v.(type) {
	case map[string]any:
		if k, _ := x["k"].(string); k == "pcall" {
			if n, ok := x["name"].(string); ok {
				into["pcall "+n] = true
			}
		}
		if k, _ := x["k"].(string); k == "defer" {
			if c, ok := x["x"].(map[string]any); ok {
				if n, ok := c["name"].(string); ok {
					into["defer "+n] = true
				}
			}
		}
		for _, e := range x {
			callKinds(e, into)
		}
	case []any:
		for _, e := range x {
			callKinds(e, into)
		}
	}
}
