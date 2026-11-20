module fieldnotes

go 1.27

toolchain go1.27.4

require (
	ergo.dev/xml v0.0.0
	github.com/cevr/ergo/modules/web v0.0.0
)

replace ergo.dev/xml => ../../modules/xml

replace github.com/cevr/ergo/modules/web => ../../modules/web
