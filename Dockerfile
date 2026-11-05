# Tahap 1: Build binary Go
FROM golang:1.23-alpine AS builder
WORKDIR /app

COPY go.mod ./
COPY main.go ./

RUN CGO_ENABLED=0 GOOS=linux go build -o loadtester main.go

# Tahap 2: Jalankan di image Alpine dengan CA Certificates
FROM alpine:latest

RUN apk add --no-cache ca-certificates curl

WORKDIR /app

COPY --from=builder /app/loadtester .

EXPOSE 8080

CMD ["./loadtester"]
