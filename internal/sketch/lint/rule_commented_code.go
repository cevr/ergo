package lint

// commentedCode is the advisory rule for code left in a comment (nits'
// commented_out_code, which guessed from code-shaped characters and
// fired on prose and worked examples): a comment group, not a doc
// comment, whose text without its comment marks parses as Ergo
// statements that are more than lone names or literals, or as
// declarations. Prose does not parse; a worked example in a doc comment
// is exempt with the doc comment.
var commentedCode = &Rule{
	Name:    "commented-code",
	Code:    "L143",
	Doc:     "A comment that parses as code is code kept out of the build: version control keeps old code.",
	Instead: "delete it; the history keeps it",
	Default: Hint,
	Check:   checkCommentedCode,
}

func checkCommentedCode(p *Pass) {
	docs := docComments(p.File)
	for _, g := range p.File.Comments {
		if docs[g] {
			continue
		}
		if parsesAsCode(commentBody(g)) {
			p.Report(g.Pos(), g.End(), "this comment parses as code")
		}
	}
}
