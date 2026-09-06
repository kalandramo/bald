module github.com/kalandramo/bald/transport/websocket

go 1.26.5

replace github.com/kalandramo/bald/transport => ..

replace github.com/kalandramo/bald/encoding => ../../encoding

replace github.com/kalandramo/bald/encoding/json => ../../encoding/json

require (
	github.com/google/uuid v1.6.0
	github.com/gorilla/websocket v1.5.3
	github.com/kalandramo/bald/encoding v0.0.1
	github.com/kalandramo/bald/encoding/json v0.0.1
	github.com/kalandramo/bald/transport v0.0.0-00010101000000-000000000000
)
