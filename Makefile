.PHONY: build vet test dist run-relay run-bridge run-http run-curl demo clean

PIKTAK_ADDR    ?= :7681
PIKTAK_INGRESS ?= :7682
PIKTAK_RELAY   ?= 127.0.0.1:7681
PIKTAK_CODES   ?= 12345678
PIKTAK_CODE    ?= 12345678
PIKTAK_LOCAL   ?= 127.0.0.1:7531

build:
	go build ./...

vet:
	go vet ./...

test:
	go test ./...

# Cross-compile the standalone Machine daemon for GitHub Releases.
dist:
	@mkdir -p bin
	@for t in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do \
		os=$${t%/*}; arch=$${t#*/}; \
		echo "building bin/piktakd-$$os-$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -o bin/piktakd-$$os-$$arch ./cmd/piktakd; \
	done
	@ls -lh bin/ | tail -n +2

run-relay:
	PIKTAK_ADDR=$(PIKTAK_ADDR) PIKTAK_INGRESS=$(PIKTAK_INGRESS) PIKTAK_CODES=$(PIKTAK_CODES) go run ./cmd/piktak-relay

run-bridge:
	PIKTAK_RELAY=$(PIKTAK_RELAY) PIKTAK_CODE=$(PIKTAK_CODE) PIKTAK_LOCAL=$(PIKTAK_LOCAL) go run ./cmd/piktakd

run-http:
	python3 -m http.server 7531

run-curl:
	curl -sS http://127.0.0.1:7682/ | head -20

demo:
	@echo "transparent bridge demo, three terminals + one curl:"
	@echo "  1: make run-relay"
	@echo "  2: make run-http"
	@echo "  3: make run-bridge"
	@echo "  4: make run-curl"

clean:
	rm -rf bin
	rm -f piktakd piktak-relay
