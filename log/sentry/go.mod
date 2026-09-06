module github.com/kalandramo/bald/log/sentry

go 1.26.5

require (
	github.com/getsentry/sentry-go v0.46.0
	github.com/kalandramo/bald/bconf v0.0.0
	github.com/kalandramo/bald/log v0.0.0
)

require (
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/text v0.36.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

replace github.com/kalandramo/bald/bconf => ../../bconf

replace github.com/kalandramo/bald/log => ..
