package rawreads

import "ergo.local/prototype/internal/sketch/lower"

// Allowed: lower.go may read the Unit's lists inside Validate and
// validateFiles, which check the Unit before it is made canonical.
func Validate(u lower.Unit) int { return len(u.Imports) + validateFiles(u) }

func validateFiles(u lower.Unit) int { return len(u.Files) + len(u.Sources) }

// 6. caught: any other function of lower.go.
func escInLowerGo(u lower.Unit) int { return len(u.Sources) }
