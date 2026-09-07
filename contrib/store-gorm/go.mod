module github.com/kalandramo/bald-store-gorm

go 1.27.1

require (
	github.com/glebarez/sqlite v1.11.0
	github.com/kalandramo/bald v0.0.0
	github.com/kalandramo/bald/bconf v0.0.0-00010101000000-000000000000
	github.com/stretchr/testify v1.12.1
	gorm.io/gorm v1.25.12
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/glebarez/go-sqlite v1.21.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	github.com/mattn/go-isatty v0.0.22 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	modernc.org/libc v1.22.5 // indirect
	modernc.org/mathutil v1.5.0 // indirect
	modernc.org/memory v1.5.0 // indirect
	modernc.org/sqlite v1.23.1 // indirect
)

replace github.com/kalandramo/bald => ../..

replace github.com/kalandramo/bald/bconf => ../../bconf
