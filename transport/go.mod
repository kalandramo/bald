module github.com/kalandramo/bald/transport

go 1.27.1

replace github.com/kalandramo/bald/broker => ../broker

replace github.com/kalandramo/bald/log => ../log

replace github.com/kalandramo/bald/metrics => ../metrics

replace github.com/kalandramo/bald/transport/subscribe => ./subscribe

require (
	github.com/kalandramo/bald/broker v0.4.1
	github.com/kalandramo/bald/log v0.6.1
	github.com/kalandramo/bald/metrics v0.2.1
	github.com/kalandramo/bald/transport/subscribe v0.1.1
	github.com/stretchr/testify v1.12.1
)

require (
	github.com/kalandramo/bald/encoding v0.2.1 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
)
