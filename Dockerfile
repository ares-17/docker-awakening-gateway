# Stage 1: Build the Go binary
FROM golang:1.24-alpine AS builder

WORKDIR /app

# Install certificates
RUN apk add --no-cache ca-certificates

# Download dependencies (cached layer — only invalidated when go.sum changes)
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o docker-gateway .

# Stage 2: Final lightweight image
FROM gcr.io/distroless/static-debian12

WORKDIR /

# Copy the binary and certificates
COPY --from=builder /app/docker-gateway /docker-gateway
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Expose the gateway port
EXPOSE 8080

# Run the binary
ENTRYPOINT ["/docker-gateway"]
