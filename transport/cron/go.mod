module github.com/kalandramo/bald/transport/cron

go 1.27.1

replace github.com/kalandramo/bald/transport => ..

require (
	github.com/kalandramo/bald/transport v0.0.0-00010101000000-000000000000
	github.com/robfig/cron/v3 v3.0.1
)
