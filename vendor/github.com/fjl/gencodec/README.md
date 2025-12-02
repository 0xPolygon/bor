Command gencodec generates marshaling methods for Go struct types.

The generated methods add features which json and other marshaling packages cannot offer.

	gencodec -dir . -type MyType -formats json,yaml,toml -out mytype_json.go

See [the documentation for more details](https://godoc.org/github.com/fjl/gencodec).
