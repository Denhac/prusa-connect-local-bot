# Build, test and package the module.
#
# Go must be the version go.mod names (1.26.11). If it is not first on PATH:
#   make GO=$$HOME/sdk/go1.26.11/bin/go release

GO        ?= go
NAME      := slack-bot
VERSION   := $(shell sed -n 's/^version: *//p' module.yaml)
# Also listed under platforms: in module.yaml; manifest_test.go checks both.
PLATFORMS := linux_arm64 linux_amd64
RELEASES  := bin/release
RELEASE   := $(RELEASES)/$(VERSION)

.PHONY: help check fmt vet test race build release clean

help:
	@echo "make check    gofmt, go vet and every test"
	@echo "make race     the tests under the race detector (needs a C compiler)"
	@echo "make build    a binary for this machine, in bin/"
	@echo "make release  check, then the release directory and archive for $(VERSION) in $(RELEASES)/"
	@echo "make clean    remove bin/"

check: fmt vet test

fmt:
	@unformatted=$$(gofmt -l .); if [ -n "$$unformatted" ]; then \
		echo "gofmt would change:"; echo "$$unformatted"; exit 1; fi

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

race:
	CGO_ENABLED=1 $(GO) test -race ./...

build:
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/$(NAME) .

# A release is the directory the server installs from: the manifest at its
# root, one binary per platform named <name>_<goos>_<goarch>, the licence, and
# checksums.txt over every other file, in sha256sum's format, sorted by name.
release: check
	rm -rf $(RELEASE)
	mkdir -p $(RELEASE)
	for platform in $(PLATFORMS); do \
		CGO_ENABLED=0 GOOS=$${platform%_*} GOARCH=$${platform#*_} \
			$(GO) build -trimpath -ldflags='-s -w' -o $(RELEASE)/$(NAME)_$$platform . || exit 1; \
	done
	cp module.yaml openapi.json LICENSE $(RELEASE)/
	cd $(RELEASE) && find . -type f ! -name checksums.txt ! -name checksums.txt.sig | \
		sed 's|^\./||' | LC_ALL=C sort | xargs sha256sum > checksums.txt
	tar -C $(RELEASES) -czf $(RELEASES)/$(NAME)-$(VERSION).tar.gz $(VERSION)
	@echo "released $(RELEASE) and $(RELEASES)/$(NAME)-$(VERSION).tar.gz"

clean:
	rm -rf bin
