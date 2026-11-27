package lint

// fileNameCase (layout): enforces a consistent case convention on Ergo file
// names (and optionally folder segments) while preserving recognized suffixes
// such as _test.ergo, _laws.ergo, _gen.ergo, and platform tags.
var fileNameCase = &Rule{
	Name:    "file-name-case",
	Code:    "L172",
	Doc:     "A checked source file name (or directory segment when dirs is enabled) does not match the configured case style (case: snake_case, kebab-case, camelCase, PascalCase, or flatcase; absent: inactive). Special suffixes such as _test.ergo, _laws.ergo, _gen.ergo, and OS/arch tags are preserved.",
	Instead: "rename the file or directory to match the configured case style",
	Default: Warning,
	Check:   checkFileNameCase,
	Options: options(func() FileNameCaseOptions { return FileNameCaseOptions{} }, ValidateFileNameCaseOptions),
}

func checkFileNameCase(p *Pass) {
	if msg, instead, ok := fileNameCaseFinding(p); ok {
		at := p.File.Pos()
		if len(p.File.Decls) > 0 {
			at = p.File.Decls[0].Pos()
		}
		p.ReportInstead(at, at, msg, instead, nil)
	}
}
