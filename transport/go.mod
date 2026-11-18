module github.com/kalandramo/bald/transport

go 1.27.1

replace github.com/kalandramo/bald/broker => ../broker

replace github.com/kalandramo/bald/log => ../log

replace github.com/kalandramo/bald/metrics => ../metrics

replace github.com/kalandramo/bald/transport/subscribe => ./subscribe

require (
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

replace github.com/kalandramo/bald/bconf => ../bconf

replace github.com/kalandramo/bald/encoding => ../encoding

replace github.com/kalandramo/bald/encoding/json => ../encoding/json

require (
	github.com/kalandramo/bald/bconf v0.28.1
	github.com/kalandramo/bald/broker v0.4.1
	github.com/kalandramo/bald/encoding v0.2.1
	github.com/kalandramo/bald/log v0.6.1
	github.com/kalandramo/bald/metrics v0.2.1
	github.com/kalandramo/bald/transport/subscribe v0.1.1
	github.com/stretchr/testify v1.12.1
)
