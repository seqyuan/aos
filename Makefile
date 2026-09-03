GOPROXY ?= https://goproxy.cn,direct
export GOPROXY

BINARY := aos

# 版本号单一来源：git tag（如 v0.3.1）；非 tag 环境回退到 commit SHA，再回退 "dev"。
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test vet clean ping linux docs docs/sync serve

build:
	go build -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/aos

linux:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/aos-linux-amd64 ./cmd/aos
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/aos-linux-arm64 ./cmd/aos

test:
	go test ./...

vet:
	go vet ./...

# 文档页从根 README/CHANGELOG 复制生成（替代软链，避免 Windows/CI 下软链失效），
# 产物被 .gitignore 忽略、不入库；改文档仍只改根文件即可。
docs/sync:
	cp README.md docs/index.md
	cp CHANGELOG.md docs/changelog.md

docs: docs/sync
	python3 -m mkdocs build

serve: docs/sync
	python3 -m mkdocs serve

clean:
	rm -f $(BINARY)
	rm -rf dist _site

ping: build
	./$(BINARY) check
