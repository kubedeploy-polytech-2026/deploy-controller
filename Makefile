.DEFAULT_GOAL := help

.PHONY: help test build check
help:
	@printf '%s\n' 'test   Run unit tests with race detection' 'build  Build the controller' 'check  Test, vet and build'
test:
	go test -race ./...
build:
	go build -o build/deploy-controller ./cmd/controller
check: test
	go vet ./...
	$(MAKE) build
