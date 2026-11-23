# ---- 构建阶段 ----
FROM golang:1.27-alpine AS build
# 模块代理：默认阿里云（与宿主机 go env GOPROXY 一致）；海外环境可用 --build-arg GOPROXY=direct 覆盖
ARG GOPROXY=https://mirrors.aliyun.com/goproxy,direct
ENV GOPROXY=${GOPROXY}
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/knight-server ./cmd/knight-server

# ---- 运行阶段 ----
FROM alpine:3.21
RUN adduser -D -u 10001 knight
USER knight

COPY --from=build /out/knight-server /app/knight-server

ENV KNIGHT_HTTP_PORT=37780
EXPOSE 37780
EXPOSE 37777/udp

ENTRYPOINT ["/app/knight-server"]
