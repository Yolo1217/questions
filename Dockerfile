FROM golang:1.25-alpine AS builder

# 默认官方代理（CI/海外可直连）；国内网络传 --build-arg GOPROXY=https://goproxy.cn,direct
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o /out/gateway ./cmd/gateway && \
    go build -o /out/user ./cmd/user && \
    go build -o /out/order ./cmd/order && \
    go build -o /out/note ./cmd/note

FROM alpine:3.22

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app
COPY --from=builder /out/ /app/
COPY configs/ /app/configs/
EXPOSE 8080 9001 9002 9100
CMD ["/app/gateway"]
