module github.com/kalandramo/bald/metrics/datadog

go 1.27.1

require (
	github.com/DataDog/datadog-go/v5 v5.8.2
	github.com/kalandramo/bald/metrics v0.1.0
)

require (
	github.com/Microsoft/go-winio v0.5.0 // indirect
	golang.org/x/sys v0.0.0-20210510120138-977fb7262007 // indirect
)

replace github.com/kalandramo/bald/metrics => ..
