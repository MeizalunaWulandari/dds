# Tahap 1: Build binary Go
FROM golang:1.23-alpine AS builder
WORKDIR /app

COPY go.mod ./ 
COPY main.go ./

RUN CGO_ENABLED=0 GOOS=linux go build -o loadtester main.go

# Tahap 2: Jalankan di image Alpine dengan CA Certificates
FROM alpine:latest

# Install sertifikat SSL agar mendukung koneksi HTTPS eksternal
RUN apk add --no-cache ca-certificates

WORKDIR /app

# Salin binary hasil build dari tahap 1
COPY --from=builder /app/loadtester .

# Ekspose port aplikasi
EXPOSE 8080

# Jalankan aplikasi
CMD ["./loadtester"]
