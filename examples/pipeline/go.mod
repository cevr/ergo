module pipeline

go 1.27

toolchain go1.27.4

require (
	github.com/cevr/ergo/modules/domain v0.0.0
	github.com/cevr/ergo/modules/infra v0.0.0
	github.com/cevr/ergo/modules/infra-local v0.0.0
	github.com/cevr/ergo/modules/mcp v0.0.0
)

replace github.com/cevr/ergo/modules/domain => ../../modules/domain

replace github.com/cevr/ergo/modules/infra => ../../modules/infra

replace github.com/cevr/ergo/modules/infra-local => ../../modules/infra-local

replace github.com/cevr/ergo/modules/mcp => ../../modules/mcp
