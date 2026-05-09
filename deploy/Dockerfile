# =============================================================================
# Trader Binary - Multi-stage build
# 不依赖 CGO (DuckDB 已砍), 镜像更小, 跨平台编译方便
# =============================================================================

FROM golang:1.23-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download

COPY . .

ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w" -o /out/trader ./cmd/trader

# =============================================================================
# Runtime
# =============================================================================

FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata curl && \
    addgroup -S trader && adduser -S trader -G trader

# 容器时区必须设(对齐应用层 BJT)
ENV TZ=Asia/Shanghai
RUN cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime && \
    echo "Asia/Shanghai" > /etc/timezone

WORKDIR /app
COPY --from=builder /out/trader /app/trader
COPY --from=builder /src/internal/storage/postgres/migrations /app/migrations

USER trader

EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD curl -fs http://localhost:8080/health || exit 1

ENTRYPOINT ["/app/trader"]
