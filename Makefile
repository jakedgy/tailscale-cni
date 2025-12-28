.PHONY: all build build-cni build-daemon proto docker clean test test-nginx install k3d k3d-create k3d-create-multi k3d-delete k3d-setup k3d-setup-multi deps fmt lint deploy undeploy logs restart

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOTEST=$(GOCMD) test
GOCLEAN=$(GOCMD) clean
GOMOD=$(GOCMD) mod

# Binary names
CNI_BINARY=tailscale-cni
DAEMON_BINARY=tailscale-cni-daemon

# Docker
IMAGE_NAME=tailscale-cni
IMAGE_TAG=latest

# Build flags
LDFLAGS=-ldflags="-s -w"

all: build

# Build both binaries
build: build-cni build-daemon

# Build CNI plugin binary
build-cni:
	CGO_ENABLED=0 $(GOBUILD) $(LDFLAGS) -o bin/$(CNI_BINARY) ./cmd/cni

# Build daemon binary
build-daemon:
	CGO_ENABLED=0 $(GOBUILD) $(LDFLAGS) -o bin/$(DAEMON_BINARY) ./cmd/daemon

# Generate protobuf code
proto:
	protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		pkg/proto/cni.proto

# Build Docker image
docker:
	docker build -t $(IMAGE_NAME):$(IMAGE_TAG) .

# Build for k3d (imports image)
K3D_CLUSTER=dev
k3d: docker
	k3d image import $(IMAGE_NAME):$(IMAGE_TAG) -c $(K3D_CLUSTER)

# Create k3d cluster (single node)
k3d-create:
	k3d cluster create $(K3D_CLUSTER) \
		--k3s-arg "--disable=servicelb@server:*" \
		--k3s-arg "--disable=traefik@server:*"

# Create multi-node k3d cluster (1 server + 2 agents)
k3d-create-multi:
	k3d cluster create $(K3D_CLUSTER) \
		--servers 1 \
		--agents 2 \
		--k3s-arg "--disable=servicelb@server:*" \
		--k3s-arg "--disable=traefik@server:*"

# Delete k3d cluster
k3d-delete:
	k3d cluster delete $(K3D_CLUSTER)

# Full k3d setup: create cluster, build image, import, deploy
k3d-setup: k3d-create docker
	k3d image import $(IMAGE_NAME):$(IMAGE_TAG) -c $(K3D_CLUSTER)
	kubectl apply -k deploy/
	@echo "Waiting for tailscale-cni daemon to be ready..."
	kubectl -n kube-system rollout status daemonset/tailscale-cni --timeout=120s
	@echo "k3d cluster ready with Tailscale CNI"

# Full k3d multi-node setup: create 3-node cluster, build image, import, deploy
k3d-setup-multi: k3d-create-multi docker
	k3d image import $(IMAGE_NAME):$(IMAGE_TAG) -c $(K3D_CLUSTER)
	kubectl apply -k deploy/
	@echo "Waiting for tailscale-cni daemon to be ready on all nodes..."
	kubectl -n kube-system rollout status daemonset/tailscale-cni --timeout=120s
	@echo "k3d multi-node cluster ready with Tailscale CNI"

# Smoke test: deploy nginx, wait for Tailscale IP, curl it
test-nginx:
	@echo "Deploying nginx test pod..."
	@kubectl run nginx-test --image=nginx:alpine --restart=Never 2>/dev/null || true
	@echo "Waiting for pod to be ready..."
	@kubectl wait --for=condition=Ready pod/nginx-test --timeout=120s
	@echo "Getting Tailscale IP..."
	@TSIP=$$(kubectl get pod nginx-test -o jsonpath='{.status.podIPs[0].ip}'); \
	echo "Tailscale IP: $$TSIP"; \
	echo "Curling nginx via Tailscale IP..."; \
	kubectl run curl-test --image=curlimages/curl:latest --restart=Never --rm -it -- curl -s --connect-timeout 10 http://$$TSIP | head -5; \
	echo ""; \
	echo "Cleaning up..."; \
	kubectl delete pod nginx-test --ignore-not-found

# Run tests (in Docker for Linux compatibility, with caching)
test:
	docker run --rm \
		-v $(PWD):/app \
		-v go-mod-cache:/go/pkg/mod \
		-v go-build-cache:/root/.cache/go-build \
		-w /app \
		golang:1.25 go test -v ./...

# Clean build artifacts
clean:
	$(GOCLEAN)
	rm -rf bin/

# Download dependencies
deps:
	$(GOMOD) download
	$(GOMOD) tidy

# Install CNI plugin locally (for testing)
install: build-cni
	sudo cp bin/$(CNI_BINARY) /opt/cni/bin/

# Format code
fmt:
	$(GOCMD) fmt ./...

# Lint code
lint:
	golangci-lint run

# Deploy to Kubernetes
deploy:
	kubectl apply -k deploy/

# Remove from Kubernetes
undeploy:
	kubectl delete -k deploy/ --ignore-not-found

# View daemon logs
logs:
	kubectl -n kube-system logs -l app=tailscale-cni -f

# Restart daemon
restart:
	kubectl -n kube-system rollout restart daemonset/tailscale-cni
