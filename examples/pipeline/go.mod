module pipeline

go 1.27

require (
	github.com/cevr/ergo/modules/domain v0.0.0
	github.com/cevr/ergo/modules/mcp v0.0.0
)

replace github.com/cevr/ergo/modules/domain => ../../modules/domain

replace github.com/cevr/ergo/modules/mcp => ../../modules/mcp
