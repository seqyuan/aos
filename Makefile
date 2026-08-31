GOPROXY ?= https://goproxy.cn,direct
export GOPROXY

BINARY := aos

.PHONY: build test vet clean ping linux docs

build:
	go build -ldflags="-s -w -X main.version=dev" -o $(BINARY) ./cmd/aos

linux:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w -X main.version=dev" -o dist/aos-linux-amd64 ./cmd/aos
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w -X main.version=dev" -o dist/aos-linux-arm64 ./cmd/aos

test:
	go test ./...

vet:
	go vet ./...

docs:
	python3 docs/build.py

clean:
	rm -f $(BINARY)
	rm -rf dist _site

ping: build
	./$(BINARY) check
