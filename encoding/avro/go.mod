module github.com/kalandramo/bald/encoding/avro

go 1.27.1

require (
	github.com/kalandramo/bald/encoding v0.1.0
	github.com/linkedin/goavro/v2 v2.15.0
)

require github.com/golang/snappy v0.0.1 // indirect

replace github.com/kalandramo/bald/encoding => ..
