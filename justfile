# Justfile for disk-exporter
# Run 'just' or 'just --list' to see all available commands
# https://github.com/casey/just

binary_name := env_var_or_default('BINARY_NAME', 'disk-exporter')
version     := env_var_or_default('VERSION', 'dev')
docker_image := env_var_or_default('DOCKER_IMAGE', 'ghcr.io/cullenmcdermott/disk-exporter')

# Default recipe — show help
default:
    @just --list

# ==========================================
# Build Commands
# ==========================================

# Build the binary (host OS/arch)
build:
    @echo "🔨 Building {{binary_name}}..."
    go build -ldflags="-s -w" -o {{binary_name}} .
    @echo "✅ Build complete: {{binary_name}}"

# Build a Linux amd64 binary (for container testing)
build-linux:
    @echo "🔨 Building {{binary_name}} for linux/amd64..."
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o {{binary_name}}-linux-amd64 .
    @echo "✅ Build complete: {{binary_name}}-linux-amd64"

# Build the container image locally
docker-build:
    @echo "🐳 Building container image..."
    docker build -t {{docker_image}}:{{version}} .
    @echo "✅ Image built: {{docker_image}}:{{version}}"

# Build and load multi-arch image locally (requires buildx)
docker-build-multiarch:
    @echo "🐳 Building multi-arch container image..."
    docker buildx build --platform linux/amd64,linux/arm64 -t {{docker_image}}:{{version}} --load .
    @echo "✅ Multi-arch image built"

# Clean build artifacts
clean:
    @echo "🧹 Cleaning build artifacts..."
    rm -f {{binary_name}} {{binary_name}}-linux-amd64
    rm -f coverage.out coverage.html
    @echo "✅ Clean complete"

# ==========================================
# Testing Commands
# ==========================================

# Run all tests
test:
    @echo "🧪 Running all tests..."
    go test -v -race ./...

# Run tests with coverage report
test-coverage:
    @echo "🧪 Running tests with coverage..."
    go test -v -race -coverprofile=coverage.out ./...
    @echo "📊 Generating coverage report..."
    go tool cover -html=coverage.out -o coverage.html
    @echo "✅ Coverage report generated: coverage.html"

# ==========================================
# Code Quality
# ==========================================

# Run all checks (fmt, vet, lint)
check: fmt vet lint
    @echo "✅ All checks passed"

# Format code
fmt:
    @echo "📝 Formatting code..."
    go fmt ./...
    @echo "✅ Format complete"

# Run go vet
vet:
    @echo "🔍 Running go vet..."
    go vet ./...
    @echo "✅ Vet complete"

# Run golangci-lint
lint:
    @echo "🔍 Running golangci-lint..."
    golangci-lint run
    @echo "✅ Lint complete"

# ==========================================
# Dependencies
# ==========================================

# Download dependencies
deps:
    @echo "📦 Downloading dependencies..."
    go mod download
    @echo "✅ Dependencies downloaded"

# Tidy dependencies
tidy:
    @echo "📦 Tidying dependencies..."
    go mod tidy
    @echo "✅ Dependencies tidied"
