module github.com/kalandramo/bald-authz-casbin

go 1.26.5

require (
	github.com/casbin/casbin/v2 v2.128.0
	github.com/kalandramo/bald v0.0.0
)

require (
	github.com/bmatcuk/doublestar/v4 v4.6.1 // indirect
	github.com/casbin/govaluate v1.3.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
)

replace github.com/kalandramo/bald => ../..
