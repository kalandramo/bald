# grpc-gateway 代码生成示例（在 _example/bald 目录下执行）。
#
# 前置工具链（本机需自行安装，bald 仓库不内置）：
#   - buf      https://buf.build/docs/installation
#   - protoc-gen-go            go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
#   - protoc-gen-go-grpc       go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
#   - protoc-gen-grpc-gateway  go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway@latest
#   - googleapis 依赖：buf mod update（首次会自动拉取 buf.build/googleapis/googleapis）
#
# 执行：
#   cd _example/bald && make proto
# 生成产物位于 gen/baldv1/（*.pb.go / *_grpc.pb.go / *_pb.gw.go）。

.PHONY: proto tidy

proto:
	cd proto && buf mod update || true
	buf generate proto --template proto/buf.gen.yaml

# 生成后把 grpc-gateway/v2 等依赖写入 go.mod（首次需执行）。
tidy:
	go mod tidy
