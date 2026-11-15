# 由 bald gen app 生成（seeded：可按团队环境改写，生成器不再覆盖）。
# 构建：docker build -f build/docker/bald-demo-worker/Dockerfile -t bald-demo-worker:dev .
# 或：task image
FROM golang:1.27 AS build
WORKDIR /src
# go.sum 在首次 go mod tidy 之前可能不存在：用通配让它可选。
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/bald-demo-worker

FROM gcr.io/distroless/static-debian12:nonroot
# WORKDIR 与 configs 一起落到 /app：appkit.ConfigFile 的 "configs/<name>.yaml"
# 是相对工作目录解析的，镜像里必须保持同样的相对位置。
WORKDIR /app
COPY --from=build /out/app /app/app
COPY --from=build /src/configs /app/configs
USER nonroot:nonroot
ENTRYPOINT ["/app/app"]
