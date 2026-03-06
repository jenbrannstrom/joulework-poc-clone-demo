FROM golang:1.23-alpine AS builder
WORKDIR /src

COPY go.mod ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/mcu ./cmd/mcu
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/local-worker ./cmd/local-worker

FROM alpine:3.21 AS runtime
WORKDIR /app
RUN adduser -D -H -u 10001 appuser
COPY --from=builder /out/mcu /usr/local/bin/mcu
COPY --from=builder /out/local-worker /usr/local/bin/local-worker
USER appuser
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/mcu"]
