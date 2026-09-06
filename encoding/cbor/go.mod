module github.com/kalandramo/bald/encoding/cbor

go 1.26.5

require (
	github.com/fxamacker/cbor/v2 v2.9.1
	github.com/kalandramo/bald/encoding v0.0.0
)

require github.com/x448/float16 v0.8.4 // indirect

replace github.com/kalandramo/bald/encoding => ..
