CORE_REPO ?= https://github.com/prusa3d/prusa-connect.git
CORE_REF ?= v0.1.0-beta.2
CORE := .core

NAME := $(shell sed -n 's/^name:[[:space:]]*//p' module.yaml | head -1)
VERSION := $(shell sed -n 's/^version:[[:space:]]*//p' module.yaml | head -1)
MODULE := $(shell sed -n 's/^module[[:space:]]*//p' go.mod | head -1)
NAMESPACE := $(shell echo $(MODULE) | sed -n 's|^github\.com/\([^/]*\)/\([^/]*\)$$|com.github.aux.\1.\2|p')
PLATFORMS := $(shell sh scripts/platforms.sh)

RELEASE := release
export CI ?= true

.PHONY: core client client-check check go-check ui-check ui-build release clean

$(CORE)/.git:
	git -c advice.detachedHead=false clone --quiet --depth 1 --branch $(CORE_REF) $(CORE_REPO) $(CORE)

$(CORE)/packages/svelte/dist/build/module.js: | $(CORE)/.git
	cd $(CORE)/packages/svelte && pnpm install --frozen-lockfile
	cd $(CORE)/packages/svelte && pnpm run build

$(CORE)/packages/typescript/dist/index.js: | $(CORE)/.git
	cd $(CORE)/packages/typescript && pnpm install --frozen-lockfile
	cd $(CORE)/packages/typescript && pnpm run build

core: $(CORE)/packages/svelte/dist/build/module.js $(CORE)/packages/typescript/dist/index.js

check: go-check client-check ui-check

go-check: core
	go vet ./...
	go test ./...

client: core
	go -C $(CORE) run ./cmd/generate-client-sdk -module -document "$(CURDIR)/openapi.json" \
		-namespace $(NAMESPACE) -name printer-hawk-client -out "$(CURDIR)/client"
	cd client && pnpm install --frozen-lockfile && pnpm run generate:types

client-check: client/node_modules/.modules.yaml
	go -C $(CORE) run ./cmd/generate-client-sdk -module -check -document "$(CURDIR)/openapi.json" \
		-namespace $(NAMESPACE) -name printer-hawk-client -out "$(CURDIR)/client"
	cd client && pnpm run generate:types && git diff --exit-code src/generated/

client/node_modules/.modules.yaml: client/package.json client/pnpm-lock.yaml | core
	cd client && pnpm install --frozen-lockfile

client/dist/index.js: client/node_modules/.modules.yaml $(wildcard client/src/*.ts client/src/generated/*.ts)
	cd client && pnpm run build

ui/node_modules/.modules.yaml: ui/package.json ui/pnpm-lock.yaml client/dist/index.js | core
	cd ui && pnpm install --frozen-lockfile

ui-check: ui/node_modules/.modules.yaml
	cd ui && pnpm run check
	cd ui && pnpm run build

ui-build: ui/node_modules/.modules.yaml
	cd ui && pnpm run build

release: ui-build core
	rm -rf $(RELEASE)
	cp -R ui/dist $(RELEASE)
	cp LICENSE README.md CHANGELOG.md $(RELEASE)/
	for p in $(PLATFORMS); do \
		os="$${p%_*}"; arch="$${p#*_}"; \
		ext=""; [ "$$os" = "windows" ] && ext=".exe"; \
		CGO_ENABLED=0 GOOS="$$os" GOARCH="$$arch" go build -trimpath -o "$(RELEASE)/$(NAME)_$${p}$${ext}" .; \
	done

clean:
	rm -rf $(RELEASE) bin ui/dist ui/node_modules client/dist client/node_modules $(CORE)
