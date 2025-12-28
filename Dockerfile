# Build stage
FROM golang:1.25-bookworm AS builder

WORKDIR /app

# Copy go mod files first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build binaries
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /tailscale-cni ./cmd/cni
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /tailscale-cni-daemon ./cmd/daemon

# Runtime stage
FROM alpine:3.19

RUN apk add --no-cache ca-certificates iptables ip6tables

# Copy binaries from builder
COPY --from=builder /tailscale-cni /tailscale-cni
COPY --from=builder /tailscale-cni-daemon /tailscale-cni-daemon

# Default command runs the daemon
ENTRYPOINT ["/tailscale-cni-daemon"]
