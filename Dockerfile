# Job Center 运行镜像（多阶段）。盒子无 Docker，本文件尚未实测，见 docs/p8-report.md。
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
COPY configs/local.yaml /app/configs/local.yaml
ENV TZ=Asia/Shanghai
EXPOSE 8080 8081
ENTRYPOINT ["/app/job-center"]
CMD ["-config", "/app/configs/local.yaml"]
