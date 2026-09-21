BINARY  := apiary-plugin-slack
BIN_DIR := bin
PLUGIN_ID := dev.apiary.slack

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "0.1.0-dev")
LDFLAGS := -ldflags "-X main.version=$(VERSION)"

.DEFAULT_GOAL := help

.PHONY: build
build: ## Build the plugin binary into bin/
	@mkdir -p $(BIN_DIR)
	go build $(LDFLAGS) -o $(BIN_DIR)/$(BINARY) ./cmd/$(BINARY)
	@echo "→ $(BIN_DIR)/$(BINARY)  ($(VERSION))"

.PHONY: test
test: ## Run all tests
	go test ./...

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: check
check: vet test build ## Vet + test + build (use in CI)

.PHONY: install
install: build ## Install into a hive: make install DIR=/path/to/.apiary/plugins
	@test -n "$(DIR)" || (echo "usage: make install DIR=<plugin_dirs entry>" && exit 1)
	@mkdir -p "$(DIR)/$(PLUGIN_ID)"
	cp $(BIN_DIR)/$(BINARY) "$(DIR)/$(PLUGIN_ID)/$(BINARY)"
	chmod +x "$(DIR)/$(PLUGIN_ID)/$(BINARY)"
	cp apiary-plugin.json "$(DIR)/$(PLUGIN_ID)/apiary-plugin.json"
	@echo "→ installed $(PLUGIN_ID) into $(DIR)"
	@echo "  pin the checksum with: make checksum DIR=$(DIR)"

.PHONY: checksum
checksum: ## Pin the installed executable's SHA-256 in its manifest
	@test -n "$(DIR)" || (echo "usage: make checksum DIR=<plugin_dirs entry>" && exit 1)
	@sum=$$(shasum -a 256 "$(DIR)/$(PLUGIN_ID)/$(BINARY)" | cut -d' ' -f1); \
	  tmp=$$(mktemp); \
	  sed "s|\"protocol\": 1,|\"protocol\": 1,\n  \"checksum\": \"sha256:$$sum\",|" \
	    "$(DIR)/$(PLUGIN_ID)/apiary-plugin.json" > $$tmp && mv $$tmp "$(DIR)/$(PLUGIN_ID)/apiary-plugin.json"; \
	  echo "→ pinned sha256:$$sum"

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'
