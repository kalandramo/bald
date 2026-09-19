module github.com/kalandramo/bald/encoding/msgpack

go 1.27.1

require (
	github.com/kalandramo/bald/encoding v0.1.0
	github.com/vmihailenco/msgpack/v5 v5.4.1
)

require github.com/vmihailenco/tagparser/v2 v2.0.0 // indirect

replace github.com/kalandramo/bald/encoding => ..
