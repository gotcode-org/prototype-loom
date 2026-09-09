# Build Stage
FROM golang:alpine AS builder

# Set the working directory
WORKDIR /app

# Copy dependency files
COPY go.mod go.sum ./
RUN go mod download

# Copy the source code
COPY . .

# Build the binary statically without CGO
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /go/bin/loom cmd/loom/main.go

# Runtime Stage
FROM alpine:latest

# Install CA certificates for HTTPS Git cloning and tzdata for timezones
RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

# Copy the binary from the builder stage
COPY --from=builder /go/bin/loom /app/loom

# Ensure the binary is executable
RUN chmod +x /app/loom

# Expose the default dev port (optional, adjust as needed for production)
EXPOSE 8080

# Default command to run the server
ENTRYPOINT ["/app/loom", "serve"]
