APP     := qocr
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -s -w -X main.version=$(VERSION)

PLATFORMS := \
	linux/amd64 \
	linux/arm64 \
	linux/arm \
	darwin/amd64 \
	darwin/arm64 \
	windows/amd64 \
	windows/arm64

.PHONY: default build-all install clean $(PLATFORMS)

default:
	$(eval OS := $(shell go env GOOS))
	$(eval ARCH := $(shell go env GOARCH))
	$(eval EXT := $(if $(filter windows,$(OS)),.exe,))
	@echo "Building for current platform ($(OS)/$(ARCH))…"
	@mkdir -p dist
	GOOS=$(OS) GOARCH=$(ARCH) CGO_ENABLED=0 \
		go build -ldflags="$(LDFLAGS)" \
		-o dist/$(APP)$(EXT) .

build-all: $(PLATFORMS)

$(PLATFORMS):
	$(eval OS   := $(word 1,$(subst /, ,$@)))
	$(eval ARCH := $(word 2,$(subst /, ,$@)))
	$(eval EXT  := $(if $(filter windows,$(OS)),.exe,))
	@echo "Building $(OS)/$(ARCH)…"
	@mkdir -p dist
	GOOS=$(OS) GOARCH=$(ARCH) CGO_ENABLED=0 \
		go build -ldflags="$(LDFLAGS)" \
		-o dist/$(APP)-$(OS)-$(ARCH)$(EXT) .

install: default
	$(eval OS := $(shell go env GOOS))
	$(eval EXT := $(if $(filter windows,$(OS)),.exe,))
	@echo "Installing to $(shell go env GOPATH)/bin/$(APP)$(EXT)…"
	@mkdir -p $(shell go env GOPATH)/bin
	@cp dist/$(APP)$(EXT) $(shell go env GOPATH)/bin/$(APP)$(EXT)

clean:
	rm -rf dist/
