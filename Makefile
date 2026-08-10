SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

ROOT := $(shell pwd)
GO := go
PYTHON := python3
PIP := $(PYTHON) -m pip

# --- toolchain paths (installed by `make tools`) -----------------------------
GOBIN := $(shell $(GO) env GOPATH)/bin
PROTOC_GEN_GO := $(GOBIN)/protoc-gen-go
PROTOC_GEN_GO_GRPC := $(GOBIN)/protoc-gen-go-grpc

PROTO_FILES := $(shell find proto -name '*.proto')

.PHONY: help
help: ## Show this help.
	@awk 'BEGIN{FS=":.*##"; printf "Targets:\n"} /^[a-zA-Z_-]+:.*##/ {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: tools
tools: ## Install Go protoc plugins.
	$(GO) install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	$(GO) install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

.PHONY: proto
proto: ## Generate Go and Python stubs from .proto files.
	@command -v protoc >/dev/null 2>&1 || { echo "protoc not found — install via: brew install protobuf"; exit 1; }
	@test -x $(PROTOC_GEN_GO) || $(MAKE) tools
	@mkdir -p api/gen/go ml/sentilyzer_ml/gen
	protoc \
		-I proto \
		--plugin=protoc-gen-go=$(PROTOC_GEN_GO) \
		--plugin=protoc-gen-go-grpc=$(PROTOC_GEN_GO_GRPC) \
		--go_out=api/gen/go --go_opt=paths=source_relative \
		--go-grpc_out=api/gen/go --go-grpc_opt=paths=source_relative \
		$(PROTO_FILES)
	cd ml && $(PYTHON) -m grpc_tools.protoc \
		-I ../proto \
		--python_out=sentilyzer_ml/gen \
		--grpc_python_out=sentilyzer_ml/gen \
		--pyi_out=sentilyzer_ml/gen \
		$(addprefix ../,$(PROTO_FILES))
	@touch ml/sentilyzer_ml/gen/__init__.py
	@find ml/sentilyzer_ml/gen -type d -exec touch {}/__init__.py \;

.PHONY: api-build
api-build: ## Build the Go API server.
	cd api && $(GO) build -o ../bin/sentilyzerd ./cmd/sentilyzerd

.PHONY: api-test
api-test: ## Run Go tests.
	cd api && $(GO) test ./... -race -count=1

.PHONY: api-run
api-run: ## Run the Go API server.
	cd api && $(GO) run ./cmd/sentilyzerd

.PHONY: ml-install
ml-install: ## Install Python ML deps into the active environment.
	cd ml && $(PIP) install -e ".[dev]"

.PHONY: ml-run
ml-run: ## Run the Python ML inference service.
	cd ml && $(if $(SENTILYZER_ML_MODEL_DIR),SENTILYZER_ML_MODEL_DIR="$(abspath $(SENTILYZER_ML_MODEL_DIR))") $(PYTHON) -m sentilyzer_ml.server

.PHONY: ml-test
ml-test: ## Run Python tests.
	cd ml && $(PYTHON) -m pytest -q

.PHONY: test
test: api-test ml-test ## Run the full test suite.

.PHONY: docker
docker: ## Build container images for both services.
	docker build -f deploy/docker/api.Dockerfile -t sentilyzer/api:dev .
	docker build -f deploy/docker/ml.Dockerfile  -t sentilyzer/ml:dev  .

.PHONY: up
up: ## Run the stack via docker compose.
	docker compose -f deploy/compose/docker-compose.yml up --build

.PHONY: down
down:
	docker compose -f deploy/compose/docker-compose.yml down -v

.PHONY: lint
lint: ## Run linters.
	cd api && $(GO) vet ./...
	cd ml && $(PYTHON) -m ruff check sentilyzer_ml tests || true

.PHONY: clean
clean:
	rm -rf bin/ api/gen/go ml/sentilyzer_ml/gen *.db
