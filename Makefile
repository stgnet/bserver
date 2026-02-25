.PHONY: build test vet lint clean install log

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

build:
	go build -ldflags "-X main.Version=$(VERSION)" -o bserver

test:
	go test ./...

vet:
	go vet ./...

lint: vet
	@echo "Lint passed (go vet)"

bench:
	go test -bench=. -benchmem ./...

clean:
	rm -f bserver

install: build
	sudo ./install-service.sh

log:
	./install-service.sh log
