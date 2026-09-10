module github.com/kalandramo/bald/transport/tcp

go 1.27.1

replace github.com/kalandramo/bald => ../../

replace github.com/kalandramo/bald/encoding => ../../encoding

replace github.com/kalandramo/bald/log => ../../log

replace github.com/kalandramo/bald/metrics => ../../metrics

replace github.com/kalandramo/bald/transport => ..

require (
	github.com/kalandramo/bald v0.0.0-00010101000000-000000000000
	github.com/kalandramo/bald/encoding v0.0.1
	github.com/kalandramo/bald/metrics v0.0.1
	github.com/kalandramo/bald/transport v0.0.0-00010101000000-000000000000
	go.opentelemetry.io/otel v1.46.0
	go.opentelemetry.io/otel/trace v1.46.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel/metric v1.46.0 // indirect
)
