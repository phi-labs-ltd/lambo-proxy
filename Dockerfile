# Build stage
FROM golang:1.24-alpine AS builder

WORKDIR /build

# Copy dependency files first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -o /app/lambo ./cmd/lambo/main.go

# Runtime stage
FROM gcr.io/distroless/static-debian12:nonroot

# Copy the binary from builder
COPY --from=builder /app/lambo /app/lambo

# Copy config file (always exists due to builder stage, but may be empty)
# Application will handle empty/missing config using env vars and defaults
COPY --from=builder --chown=nonroot:nonroot /app/config.yaml /app/config.yaml

# Expose the default proxy port
EXPOSE 8080

# Set working directory
WORKDIR /app

# Run the binary with default config path
ENTRYPOINT ["/app/lambo"]
