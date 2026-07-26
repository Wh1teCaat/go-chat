FROM golang:1.25-alpine AS builder

WORKDIR /src

ENV GOPROXY=https://goproxy.cn,direct

RUN apk add --no-cache ca-certificates git

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# 三种部署形态共用一个镜像：chat（单体）、chat-logic + chat-gateway（拆分），由 compose 的 command 选择。
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/chat ./cmd \
	&& CGO_ENABLED=0 GOOS=linux go build -o /out/chat-logic ./cmd/logic \
	&& CGO_ENABLED=0 GOOS=linux go build -o /out/chat-gateway ./cmd/gateway

FROM alpine:3.21

WORKDIR /app

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /out/chat /app/chat
COPY --from=builder /out/chat-logic /app/chat-logic
COPY --from=builder /out/chat-gateway /app/chat-gateway
COPY configs /app/configs
COPY migrations /app/migrations

EXPOSE 8080 8081 9090

CMD ["/app/chat"]
