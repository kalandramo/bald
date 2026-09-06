module github.com/kalandramo/bald/transport/webrtc

go 1.26.5

replace github.com/kalandramo/bald/broker => ../../broker

replace github.com/kalandramo/bald/encoding => ../../encoding

replace github.com/kalandramo/bald/encoding/json => ../../encoding/json

replace github.com/kalandramo/bald/encoding/proto => ../../encoding/proto

replace github.com/kalandramo/bald/log => ../../log

replace github.com/kalandramo/bald/metrics => ../../metrics

replace github.com/kalandramo/bald/transport => ..

replace github.com/tx7do/go-utils => ../../../go-utils

replace github.com/tx7do/go-utils/id => ../../../go-utils/id

require (
	github.com/kalandramo/bald/broker v0.0.0-00010101000000-000000000000
	github.com/kalandramo/bald/encoding v0.0.1
	github.com/kalandramo/bald/log v0.0.0-00010101000000-000000000000
	github.com/kalandramo/bald/metrics v0.0.1
	github.com/pion/webrtc/v4 v4.2.12
	github.com/tx7do/go-utils/id v0.0.6
)

require (
	github.com/bwmarrin/snowflake v0.3.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/kalandramo/bald/encoding/json v0.0.1 // indirect
	github.com/kalandramo/bald/encoding/proto v0.0.1 // indirect
	github.com/lithammer/shortuuid/v4 v4.2.0 // indirect
	github.com/pion/datachannel v1.6.0 // indirect
	github.com/pion/dtls/v3 v3.1.4 // indirect
	github.com/pion/ice/v4 v4.2.5 // indirect
	github.com/pion/interceptor v0.1.45 // indirect
	github.com/pion/logging v0.2.4 // indirect
	github.com/pion/mdns/v2 v2.1.0 // indirect
	github.com/pion/randutil v0.1.0 // indirect
	github.com/pion/rtcp v1.2.16 // indirect
	github.com/pion/rtp v1.10.2 // indirect
	github.com/pion/sctp v1.9.5 // indirect
	github.com/pion/sdp/v3 v3.0.18 // indirect
	github.com/pion/srtp/v3 v3.0.10 // indirect
	github.com/pion/stun/v3 v3.1.5 // indirect
	github.com/pion/transport/v4 v4.0.2 // indirect
	github.com/pion/turn/v5 v5.0.10 // indirect
	github.com/rs/xid v1.6.0 // indirect
	github.com/segmentio/ksuid v1.0.4 // indirect
	github.com/sony/sonyflake v1.3.0 // indirect
	github.com/stretchr/testify v1.12.1 // indirect
	github.com/tx7do/go-utils v1.1.40 // indirect
	github.com/wlynxg/anet v0.0.5 // indirect
	go.mongodb.org/mongo-driver/v2 v2.6.0 // indirect
	golang.org/x/crypto v0.51.0 // indirect
	golang.org/x/net v0.54.0 // indirect
	golang.org/x/sys v0.44.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)
