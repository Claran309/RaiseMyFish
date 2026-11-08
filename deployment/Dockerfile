# RaiseMyFish 运行镜像：只包含 fishd/fishctl 与采集脚本运行时，
# PostgreSQL 不在镜像内，连接串由 FISH_DB_DSN 指向外部实例。
# 配置文件不烤进镜像：运行时必须挂载 config/fishd.toml（见 deployment/docker-compose.yml）。

# ── 构建阶段 ──────────────────────────────────────────────
FROM golang:1.27-bookworm AS build
# 默认走国内模块代理；海外构建可传 --build-arg GOPROXY=https://proxy.golang.org,direct
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/fishd ./cmd/fishd \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/fishctl ./cmd/fishctl

# ── 运行阶段 ──────────────────────────────────────────────
# Node 24 提供全局 WebSocket/fetch，供 tools/bili-whale 下的采集/发送脚本使用；
# ffmpeg/ffprobe 供 [watch]/[proactive] 截帧与音频抽取使用。
FROM node:24-bookworm-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl ffmpeg tzdata \
 && rm -rf /var/lib/apt/lists/*

ENV TZ=Asia/Shanghai
WORKDIR /app

COPY --from=build /out/fishd /out/fishctl /app/
COPY personas /app/personas
COPY tools/bili-whale /app/tools/bili-whale

# 容器以 uid 1000 的 node 用户运行；这些目录也被 compose 的命名卷覆盖。
RUN mkdir -p /app/config /app/logs /app/data /app/state \
 && chown -R node:node /app

USER node
EXPOSE 8090

# 配置与 .env 由运行时挂载/注入；未提供 .env 时 godotenv 跳过缺失文件。
ENTRYPOINT ["/app/fishd"]
CMD ["--config", "config/fishd.toml", "--env-file", ".env"]
