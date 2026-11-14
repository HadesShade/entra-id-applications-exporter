BINARY  := entra-id-applications-exporter
PKG     := ./cmd/entra-id-applications-exporter
VERSION ?= dev
LDFLAGS := -s -w

.PHONY: tidy build run test lint docker release clean

tidy:
	go mod tidy

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/$(BINARY) $(PKG)

run:
	go run $(PKG) --config-file config.yaml

test:
	go test ./...

lint:
	go vet ./...

docker:
	docker build -t $(BINARY):$(VERSION) .

# Manual release archives for GitHub Releases:
#   make release VERSION=v0.1.0
# Produces dist/*.tar.gz and dist/checksums.txt for linux/amd64 and linux/arm64.
release: clean
	@for arch in amd64 arm64; do \
		name=$(BINARY)_$(VERSION)_linux_$$arch; \
		mkdir -p dist/$$name; \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -ldflags="$(LDFLAGS)" -o dist/$$name/$(BINARY) $(PKG) || exit 1; \
		cp LICENSE README.md config.example.yaml dist/$$name/; \
		tar -C dist -czf dist/$$name.tar.gz $$name; \
		rm -r dist/$$name; \
	done
	cd dist && sha256sum *.tar.gz > checksums.txt

clean:
	rm -rf dist