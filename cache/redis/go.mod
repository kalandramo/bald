module github.com/kalandramo/bald/cache/redis

go 1.27.1

require (
	github.com/alicebob/miniredis/v2 v2.35.0
	github.com/kalandramo/bald/bconf v0.0.0
	github.com/kalandramo/bald/cache v0.0.0
	github.com/redis/go-redis/v9 v9.17.2
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/yuin/gopher-lua v1.1.1 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

replace github.com/kalandramo/bald/cache => ..

replace github.com/kalandramo/bald/bconf => ../../bconf
