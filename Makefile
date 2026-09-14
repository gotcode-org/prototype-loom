.PHONY: all tidy build run test test-race lint clean

# Default target
all: tidy lint test build

# Tidy Go modules
tidy:
	@echo "Tidying go.mod..."
	go mod tidy

# Build the Loom binary
build:
	@echo "Building Loom..."
	@mkdir -p bin
	@mkdir -p .build-tmp
	TMPDIR="$(CURDIR)/.build-tmp" CGO_ENABLED=0 go build -a -installsuffix cgo -o bin/loom cmd/loom/main.go
	@rm -rf .build-tmp

# Run the Loom server in dev mode
run:
	@echo "Running Loom in dev mode..."
	go run cmd/loom/main.go serve --dev

# Run unit tests
test:
	@echo "Running tests..."
	go test -v ./...

# Run unit tests with the race detector enabled
test-race:
	@echo "Running tests with race detector..."
	go test -v -race ./...

# Run golangci-lint
lint:
	@echo "Running linters..."
	golangci-lint run ./...

# Clean build artifacts
clean:
	@echo "Cleaning up..."
	@rm -rf bin/
	go clean
