module github.com/kalandramo/bald-store-gorm

go 1.26.5

require (
	github.com/kalandramo/bald v0.0.0
	github.com/stretchr/testify v1.12.1
	gorm.io/driver/sqlite v1.5.7
	gorm.io/gorm v1.25.12
)

require (
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	github.com/mattn/go-sqlite3 v1.14.22 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/text v0.37.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

replace github.com/kalandramo/bald => ../..
