package lint

// conceptFileNames is the layout rule for files named by concept, not by
// kind (AGENTS.md: "a package per responsibility with files named by
// concept", owner 2026-11-04): a file name that ends with a kind word
// (user_service.ergo, store.layer.ergo). Option suffixes lists the kind
// words.
var conceptFileNames = &Rule{
	Name:    "concept-file-names",
	Code:    "L113",
	Doc:     "A source file named for a kind of declaration (_service, _layer, _handler, …) instead of a concept; name it for what it holds.",
	Default: Warning,
	Check:   checkConceptFileNames,
	Options: options(func() suffixOptions {
		return suffixOptions{Suffixes: []string{
			"service", "layer", "live", "repository", "repo", "handler", "controller", "impl", "adapter",
			"aggregate", "command", "event", "policy", "projection", "saga", "view", "schema", "model",
			"types", "utils", "helpers",
		}}
	}, nil),
}

func checkConceptFileNames(p *Pass) {
	if p.Package.Std() || p.Package.Host() {
		return
	}
	if kind, ok := kindSuffix(p.Rel(), p.Options.(suffixOptions).Suffixes); ok {
		at := p.File.Pos()
		p.Report(at, at, "this file is named for a kind ("+kind+"), not a concept; name it for what it holds (AGENTS.md, Go layout)")
	}
}
