module github.com/kalandramo/bald/transport/tcp

go 1.27.1

replace github.com/kalandramo/bald/encoding => ../../encoding

replace github.com/kalandramo/bald/log => ../../log

replace github.com/kalandramo/bald/metrics => ../../metrics

replace github.com/kalandramo/bald/transport => ..

replace github.com/kalandramo/bald-utils/id => ../../../bald-utils/id

replace github.com/kalandramo/bald-utils => ../../../bald-utils

require (
	github.com/kalandramo/bald-utils/id v0.0.0-00010101000000-000000000000
	github.com/kalandramo/bald/encoding v0.0.1
	github.com/kalandramo/bald/metrics v0.0.1
	github.com/kalandramo/bald/transport v0.0.0-00010101000000-000000000000
	go.opentelemetry.io/otel v1.43.0
	go.opentelemetry.io/otel/trace v1.43.0
)

require (
	github.com/bwmarrin/snowflake v0.3.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/kalandramo/bald-utils v1.1.38 // indirect
	github.com/lithammer/shortuuid/v4 v4.2.0 // indirect
	github.com/rs/xid v1.6.0 // indirect
	github.com/segmentio/ksuid v1.0.4 // indirect
	github.com/sony/sonyflake v1.3.0 // indirect
	github.com/stretchr/testify v1.12.1 // indirect
	go.mongodb.org/mongo-driver/v2 v2.6.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel/metric v1.43.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
)
