GOPROXY ?= https://goproxy.cn,direct
export GOPROXY

BINARY := annotos

.PHONY: build test vet clean ping

build:
	go build -o $(BINARY) .

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -f $(BINARY)

ping: build
	./$(BINARY) check
