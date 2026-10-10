.PHONY: build test vet lint clean install log

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# Sources live in src/, but the binary is still written to the repo root so
# install-service.sh, systemd/launchd and `make install` find it where they
# always have.
build:
	go build -ldflags "-X main.Version=$(VERSION)" -o bserver ./src

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
