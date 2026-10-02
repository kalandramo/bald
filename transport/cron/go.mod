module github.com/kalandramo/bald/transport/cron

go 1.27.1

replace github.com/kalandramo/bald/transport => ..

replace github.com/kalandramo/bald/bconf => ../../bconf

require (
	github.com/kalandramo/bald/bconf v0.7.2
	github.com/kalandramo/bald/transport v0.2.1
	github.com/robfig/cron/v3 v3.0.1
)

require google.golang.org/protobuf v1.36.11 // indirect
