package fix

// optionTypeFor maps each Unit-10 prelude Option type/variant name to its
// canonical std package `option` (NO-PRELUDE §5.2, §6 step 6d, Unit 10).
// Unit 8 std types and Unit 9 machines are intentionally excluded from this pass.
func optionTypeFor(name string) (stdTypeSpec, bool) {
	switch name {
	case "Option", "Some", "None":
		return stdTypeSpec{pkg: "option", target: name}, true
	}
	return stdTypeSpec{}, false
}

// OptionSource rewrites every Unit-10 prelude Option/Some/None reference in
// standalone Ergo source src to its qualified `option.<Name>` form, adding
// `import option` in sorted order when needed. References resolved to
// user-declared types, type parameters, or locals (PRELUDE SHADOW) are left
// untouched. When src is already formatted, the returned text is formatted to
// a fixpoint.
func OptionSource(src, name string) (string, []StdTypeRewrite, error) {
	return stdTypesSourceWith(src, name, optionTypeFor)
}

// OptionPackage rewrites every Unit-10 prelude Option/Some/None reference in
// the package at dir over texts (keyed by absolute file path). Rewrites are
// kept only when the rewritten package checks with the same diagnostics.
func OptionPackage(dir string, texts map[string]string) (map[string]string, []StdTypeRewrite, error) {
	return stdTypesPackageWith(dir, texts, optionTypeFor)
}

// RunOption rewrites Unit-10 prelude Option/Some/None references across
// opts.Paths (module packages and standalone .ergo files) and opts.StdRoot
// (if set). Generated folders, gostd, and unadmitted folders (e.g. testdata)
// are skipped when walking a parent folder. Files are written only when
// opts.Write.
func RunOption(opts Options) (StdTypesReport, error) {
	return runStdTypesWith(opts, optionTypeFor)
}
