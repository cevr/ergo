package fix

// machineTypeFor maps each Unit-9 prelude machine type/variant name to its
// destination std package `machines` (NO-PRELUDE §5.2, §6 step 6c, Unit 9).
// Unit 8 std types and Unit 10 Option are intentionally excluded from this pass.
func machineTypeFor(name string) (stdTypeSpec, bool) {
	switch name {
	case "Enter", "Stay", "Emit", "Ignore", "Reject", "Done", "Tell",
		"Decision", "Timeout", "Machine", "InputMachine":
		return stdTypeSpec{pkg: "machines", target: name}, true
	}
	return stdTypeSpec{}, false
}

// MachinesSource rewrites every Unit-9 prelude machine type/variant reference
// in standalone Ergo source src to its qualified `machines.<Name>` form,
// adding `import machines` in sorted order when needed. References resolved to
// user-declared types, type parameters, or locals (PRELUDE SHADOW) are left
// untouched. When src is already formatted, the returned text is formatted to
// a fixpoint.
func MachinesSource(src, name string) (string, []StdTypeRewrite, error) {
	return stdTypesSourceWith(src, name, machineTypeFor)
}

// MachinesPackage rewrites every Unit-9 prelude machine type/variant reference
// in the package at dir over texts (keyed by absolute file path). Rewrites are
// kept only when the rewritten package checks with the same diagnostics.
func MachinesPackage(dir string, texts map[string]string) (map[string]string, []StdTypeRewrite, error) {
	return stdTypesPackageWith(dir, texts, machineTypeFor)
}

// RunMachines rewrites Unit-9 prelude machine type/variant references across
// opts.Paths (module packages and standalone .ergo files) and opts.StdRoot
// (if set). Generated folders, gostd, and unadmitted folders (e.g. testdata)
// are skipped when walking a parent folder. Files are written only when
// opts.Write.
func RunMachines(opts Options) (StdTypesReport, error) {
	return runStdTypesWith(opts, machineTypeFor)
}
