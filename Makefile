BINARY := build/rehearse
PORT ?= 14194
VERSION ?= dev

.PHONY: build e2e-server lint test test-browser test-race web-build

test:
	go test ./...
	npm --prefix web run test

test-race:
	go test -race ./...

lint:
	go vet ./...
	npm --prefix web run lint

web-build:
	npm --prefix web run build

build: web-build
	mkdir -p build
	go build -trimpath -ldflags "-X main.version=$(VERSION)" -o $(BINARY) ./cmd/rehearse

test-browser:
	npm --prefix web run test:e2e

e2e-server: build
	./$(BINARY) --addr 127.0.0.1:$(PORT)
