module github.com/kalandramo/bald/cache/local

go 1.27.1

require (
	github.com/coocood/freecache v1.2.7
	github.com/kalandramo/bald/bconf v0.7.2
	github.com/kalandramo/bald/cache v0.1.1
)

require (
	github.com/cespare/xxhash/v2 v2.1.2 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

replace github.com/kalandramo/bald/cache => ..

replace github.com/kalandramo/bald/bconf => ../../bconf
