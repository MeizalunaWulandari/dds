# Tahap 1: Build binary Go
FROM golang:1.22-alpine AS builder

WORKDIR /app

# Salin dependency dan source code
COPY go.mod ./ 
COPY main.go ./

# Jika menggunakan go mod (inisialisasi go.mod terlebih dahulu jika belum ada)
# RUN go mod init loadtester

# Compile binary menjadi file static murni
RUN CGO_ENABLED=0 GOOS=linux go build -o loadtester main.go

# Tahap 2: Jalankan di image yang sangat ringan (Alpine)
FROM alpine:latest

WORKDIR /app

# Salin binary hasil build dari tahap 1
COPY --from=builder /app/loadtester .

# Ekspose port aplikasi
EXPOSE 8080

# Jalankan aplikasi
CMD ["./loadtester"]