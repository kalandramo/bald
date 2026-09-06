module github.com/kalandramo/bald-database-mongodb

go 1.26.5

require (
	github.com/kalandramo/bald-crud/mongodb v0.0.0
	github.com/kalandramo/bald/bconf v0.0.0
)

require (
	github.com/jinzhu/copier v0.4.0 // indirect
	github.com/kalandramo/bald-crud/pagination v0.0.15 // indirect
	github.com/kalandramo/bald-crud/viewer v0.0.6 // indirect
	github.com/klauspost/compress v1.19.2 // indirect
	github.com/tx7do/go-utils v1.1.40 // indirect
	github.com/tx7do/go-utils/mapper v0.0.3 // indirect
	github.com/tx7do/go-wind v0.0.2 // indirect
	github.com/tx7do/go-wind-plugins/encoding v0.0.1 // indirect
	github.com/tx7do/go-wind-plugins/encoding/json v0.0.1 // indirect
	github.com/xdg-go/pbkdf2 v1.0.0 // indirect
	github.com/xdg-go/scram v1.2.0 // indirect
	github.com/xdg-go/stringprep v1.0.4 // indirect
	github.com/youmark/pkcs8 v0.0.0-20240726163527-a2c0da244d78 // indirect
	go.einride.tech/aip v0.86.3 // indirect
	go.mongodb.org/mongo-driver/v2 v2.8.0 // indirect
	golang.org/x/crypto v0.55.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260810153831-ec0a7760b754 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260810153831-ec0a7760b754 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

// bald-crud 子包是嵌套 module，其内部 replace（=> ../pagination 等）对
// 消费方不生效——此处镜像整条本地 replace 链，否则 tidy 走网络拉不存在的 tag。
replace github.com/kalandramo/bald-crud/mongodb => ../../../../bald-crud/mongodb

replace github.com/kalandramo/bald-crud/pagination => ../../../../bald-crud/pagination

replace github.com/kalandramo/bald-crud/viewer => ../../../../bald-crud/viewer

replace github.com/kalandramo/bald-crud => ../../../../bald-crud

replace github.com/kalandramo/bald/bconf => ../../../bconf
