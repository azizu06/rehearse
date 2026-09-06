BINARY := build/rehearse
GOLANGCI_LINT_VERSION := v2.12.2
GOVULNCHECK_VERSION := v1.6.0
PORT ?= 14194
TRIVY ?= trivy
TRIVY_VERSION := 0.72.0
VERSION ?= dev
RESTIC_TEST_BINARY ?= restic
TEST_PACKAGES ?= 2
TEST_PARALLEL ?= 2

.PHONY: build e2e-server lint security static test test-adapters test-adapters-race test-browser test-integration test-race trivy web-build

test:
	go test -p=$(TEST_PACKAGES) -parallel=$(TEST_PARALLEL) ./...
	npm --prefix web run test

test-race:
	go test -race -p=$(TEST_PACKAGES) -parallel=$(TEST_PARALLEL) ./...

test-adapters:
	@command -v "$(RESTIC_TEST_BINARY)" >/dev/null || { echo "restic >= 0.18.0 is required"; exit 1; }
	RESTIC_TEST_BINARY="$$(command -v "$(RESTIC_TEST_BINARY)")" go test -p=$(TEST_PACKAGES) -parallel=$(TEST_PARALLEL) -tags=integration ./internal/source/restic

test-adapters-race:
	@command -v "$(RESTIC_TEST_BINARY)" >/dev/null || { echo "restic >= 0.18.0 is required"; exit 1; }
	RESTIC_TEST_BINARY="$$(command -v "$(RESTIC_TEST_BINARY)")" go test -race -p=$(TEST_PACKAGES) -parallel=$(TEST_PARALLEL) -tags=integration ./internal/source/restic

test-integration:
	go test -p=$(TEST_PACKAGES) -parallel=$(TEST_PARALLEL) -tags=integration ./internal/probe -run TestPostgreSQLProbeEnforcesReadOnlySingleStatementAndLeastPrivilege -count=1

lint:
	go vet ./...
	npm --prefix web run lint

static:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run ./...

security: trivy
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

trivy:
	@command -v $(TRIVY) >/dev/null || { echo "Trivy $(TRIVY_VERSION) is required (set TRIVY=/path/to/trivy)"; exit 1; }
	@$(TRIVY) --version | grep -q "Version: $(TRIVY_VERSION)" || { echo "Trivy $(TRIVY_VERSION) is required"; exit 1; }
	$(TRIVY) fs --scanners vuln,secret --severity HIGH,CRITICAL --exit-code 1 --no-progress --skip-dirs .git --skip-dirs build --skip-dirs web/node_modules .

web-build:
	npm --prefix web run build

build: web-build
	mkdir -p build
	go build -trimpath -ldflags "-X main.version=$(VERSION)" -o $(BINARY) ./cmd/rehearse

test-browser:
	npm --prefix web run test:e2e

e2e-server: build
	./$(BINARY) --addr 127.0.0.1:$(PORT)
