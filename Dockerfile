# syntax=docker/dockerfile:1.7

############################
# STAGE 1 — Build
############################
FROM golang:1.26-alpine3.23 AS builder

WORKDIR /app

# Install minimal tools jika diperlukan
RUN apk add --no-cache ca-certificates tzdata

# Copy module files first (cache optimization)
COPY go.mod go.sum* ./

# Download dependencies (cacheable)
RUN go mod download

# Copy source code
COPY . .

# Build binary (small + static)
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build \
    -trimpath \
    -ldflags="-s -w" \
    -o app .

############################
# STAGE 2 — Runtime
############################
FROM scratch

WORKDIR /

# copy SSL certs (untuk HTTP client jika diperlukan)
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# copy binary
COPY --from=builder /app/app /app

# expose port
EXPOSE 8080

# run binary
ENTRYPOINT ["/app"]