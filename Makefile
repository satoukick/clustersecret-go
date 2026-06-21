# Default image ref — override on the command line, e.g.
#   make docker-build IMG=ghcr.io/you/clustersecret-go:v0.1.0
IMG ?= ghcr.io/satoukick/clustersecret-go:latest
# VERSION is stamped into the binary via -ldflags. Falls back to "dev" when
# there are no git tags yet.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# controller-gen is invoked via `go run` so no global install is needed.
CONTROLLER_GEN := go run sigs.k8s.io/controller-tools/cmd/controller-gen@latest

# Tell go tools this is a module root (helps when make is run from a subdir).
export GO111MODULE := on

.PHONY: all
all: build

##@ Development

.PHONY: fmt vet
fmt:
	go fmt ./...

vet:
	go vet ./...

.PHONY: generate
generate: ## Regenerate DeepCopy methods after editing api/v1 types.
	$(CONTROLLER_GEN) object paths="./api/v1/"

##@ Build

.PHONY: build
build: ## Compile all packages.
	go build -trimpath -ldflags "-s -w -X github.com/satoukick/clustersecret-go/cmd.version=$(VERSION)" -o bin/manager ./cmd

.PHONY: run
run: ## Run the operator locally against the current kubeconfig.
	go run ./cmd

##@ Test

.PHONY: test
test: ## Run unit tests.
	go test ./...

##@ Docker

.PHONY: docker-build
docker-build: ## Build a single-arch image for the local platform.
	docker build --build-arg VERSION=$(VERSION) -t $(IMG) .

.PHONY: docker-push
docker-push: ## Push a previously built image.
	docker push $(IMG)

.PHONY: docker-buildx
docker-buildx: ## Build and push a multi-arch (amd64+arm64) image. Requires `docker buildx`.
	docker buildx build \
		--build-arg VERSION=$(VERSION) -t $(IMG) .

##@ Deploy

.PHONY: deploy
deploy: ## Apply CRD, RBAC, and Deployment to the current cluster.
	kubectl apply -f deploy/namespace.yaml
	kubectl apply -f deploy/deployment.yaml
	kubectl apply -f config/crd/
	kubectl apply -f config/rbac/


.PHONY: undeploy
undeploy: ## Remove the operator (CRD deletion also deletes ClusterSecret resources).
	-kubectl delete -f deploy/ --ignore-not-found
	-kubectl delete -f config/rbac/ --ignore-not-found
	-kubectl delete -f config/crd/ --ignore-not-found

##@ Cleanup

.PHONY: clean
clean:
	rm -rf bin/
