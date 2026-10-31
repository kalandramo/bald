module github.com/kalandramo/bald/transport/http3

go 1.27.1

replace github.com/kalandramo/bald/log => ../../log

replace github.com/kalandramo/bald/transport => ..

require (
	github.com/gorilla/mux v1.8.1
	github.com/kalandramo/bald/log v0.5.1
	github.com/quic-go/quic-go v0.59.1
	github.com/stretchr/testify v1.12.1
)

require (
	github.com/quic-go/qpack v0.6.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/crypto v0.57.0 // indirect
	golang.org/x/net v0.60.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
)
