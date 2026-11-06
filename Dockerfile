# Job Center 运行镜像（多阶段）。默认通过 JC_* 环境变量注入配置，也可挂载 YAML 传 -config。
FROM golang:1.27-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/job-center ./cmd/server

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata \
  && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=build /out/job-center /app/job-center
COPY configs/config.example.yaml /app/configs/config.example.yaml
ENV TZ=Asia/Shanghai
EXPOSE 8080 8081
ENTRYPOINT ["/app/job-center"]
