package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ergo.local/prototype/internal/sketchfront"
)

const inspectMappersHelp = `Usage: ergo inspect mappers [--json] <path>

Print check-time content mapper outputs (file-level asset imports and inline
derive expansions), including mapped Ergo source code, source span maps,
diagnostics, step counts, and lawcache hit status.

Flags:
  --json    Print structured JSON instead of human-readable text.
  -h, --help  Show this help.
`

func inspectMappersCommand(args []string) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, err := os.Stdout.WriteString(inspectMappersHelp)
		return err
	}
	asJSON := false
	var target string
	for _, arg := range args {
		switch arg {
		case "--json":
			asJSON = true
		default:
			if strings.HasPrefix(arg, "-") {
				return unknownOption("inspect mappers", arg)
			}
			if target != "" {
				return usageError{message: "inspect mappers: expected a single file or package folder"}
			}
			target = arg
		}
	}
	if target == "" {
		target = "."
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	var a sketchfront.Analysis
	info, statErr := os.Stat(abs)
	if statErr == nil && info.IsDir() {
		a, err = sketchfront.CheckDir(abs)
		if err != nil {
			return err
		}
	} else {
		data, rErr := os.ReadFile(abs)
		if rErr != nil {
			return rErr
		}
		a = sketchfront.Check(string(data))
	}

	res := a.InspectMappers()
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		if err := enc.Encode(res); err != nil {
			return err
		}
	} else {
		writeInspectMappersText(os.Stdout, res)
	}
	if len(a.Diagnostics) > 0 {
		return exitStatusError{code: 1, message: fmt.Sprintf("inspect mappers: %s", a.Diagnostics[0].Message)}
	}
	return nil
}

func writeInspectMappersText(w *os.File, res sketchfront.InspectMappersResult) {
	if len(res.Mappers) == 0 {
		fmt.Fprintf(w, "no content mappers executed in %s\n", res.Package)
		return
	}
	for i, m := range res.Mappers {
		if i > 0 {
			fmt.Fprintln(w)
		}
		mapperName := m.MediaKind
		if m.MapperPkg != "" && m.MapperFunc != "" {
			mapperName = m.MapperPkg + "." + m.MapperFunc
		}
		origin := m.FileName
		if m.Kind == "file" && m.AssetPath != "" {
			origin = m.AssetPath
		} else if m.Line > 0 {
			origin = fmt.Sprintf("%s:%d:%d", m.FileName, m.Line, m.Col)
		}
		cacheState := "miss"
		if m.CacheHit {
			cacheState = "hit"
		}
		fmt.Fprintf(w, "mapper %s (%s) via %s [%s] cache=%s steps=%d\n",
			m.Target, m.Kind, mapperName, origin, cacheState, m.Steps)
		if m.CacheKey != "" {
			fmt.Fprintf(w, "  cache_key: %s\n", m.CacheKey)
		}
		if len(m.Decls) > 0 {
			fmt.Fprintf(w, "  decls: %s\n", strings.Join(m.Decls, ", "))
		}
		if len(m.Spans) > 0 {
			fmt.Fprintf(w, "  spans (%d):\n", len(m.Spans))
			for _, sp := range m.Spans {
				srcOrigin := fmt.Sprintf("primary[%d:%d)", sp.SrcStart, sp.SrcEnd)
				if sp.HasCompanion {
					comp := sp.CompanionPath
					if comp == "" {
						comp = fmt.Sprintf("companion#%d", sp.Companion)
					}
					srcOrigin = fmt.Sprintf("%s[%d:%d)", comp, sp.SrcStart, sp.SrcEnd)
				}
				label := sp.Label
				if label == "" {
					label = "-"
				}
				fmt.Fprintf(w, "    gen[%d:%d) <- %s (%s)\n", sp.GenStart, sp.GenEnd, srcOrigin, label)
			}
		}
		if len(m.Diags) > 0 {
			fmt.Fprintf(w, "  diags (%d):\n", len(m.Diags))
			for _, d := range m.Diags {
				sev := "error"
				if d.Warning {
					sev = "warning"
				}
				fmt.Fprintf(w, "    %s %s (%s:%d:%d): %s\n", sev, d.Code, d.MappedFile, d.MappedLine, d.MappedCol, d.Message)
			}
		}
		if m.Generated != "" {
			fmt.Fprintf(w, "  generated:\n")
			for _, line := range strings.Split(strings.TrimRight(m.Generated, "\n"), "\n") {
				fmt.Fprintf(w, "    %s\n", line)
			}
		}
	}
}
