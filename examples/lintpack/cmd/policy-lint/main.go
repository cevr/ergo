// Command policy-lint serves the example policy pack over the rule-pack
// process protocol. The manifest policy.json names its built program,
// bin/policy-lint:
//
//	go build -o examples/lintpack/bin/policy-lint ./examples/lintpack/cmd/policy-lint
//	ergo lint test examples/lintpack/testdata --lint-config examples/lintpack/lint.json
package main

import (
	"ergo.local/prototype/examples/lintpack/policy"
	"ergo.local/prototype/lint"
)

func main() { lint.Main(policy.Pack) }
