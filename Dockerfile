FROM golang:1.25-alpine AS builder

WORKDIR /src

ENV GOPROXY=https://goproxy.cn,direct

RUN apk add --no-cache ca-certificates git

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/chat ./cmd

FROM alpine:3.21

WORKDIR /app

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /out/chat /app/chat
COPY configs /app/configs
COPY migrations /app/migrations

EXPOSE 8080

CMD ["/app/chat"]
