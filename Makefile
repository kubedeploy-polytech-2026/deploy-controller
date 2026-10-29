.DEFAULT_GOAL := help
IMAGE ?= kubedeploy-controller:dev

.PHONY: help test build check image smoke
help:
	@printf '%s\n' 'test   Run unit tests with race detection' 'build  Build the controller' 'check  Test, vet and build'
	@printf '%s\n' 'image  Build the controller image' 'smoke  Check the container runtime'
test:
	go test -race ./...
build:
	go build -o build/deploy-controller ./cmd/controller
check: test
	go vet ./...
	$(MAKE) build

image:
	docker build --tag $(IMAGE) .

smoke:
	IMAGE='$(IMAGE)' bash scripts/test-image.sh
