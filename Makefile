.PHONY: run dev build build-arm64 build-amd64 tidy fmt vet test clean

BIN_DIR  := bin
VASK     := $(BIN_DIR)/vask
BACKFILL := $(BIN_DIR)/vask-embed-backfill
WEB      := $(BIN_DIR)/vask-web

run:
	go run ./cmd/vask

dev: run

build:
	mkdir -p $(BIN_DIR)
	go build -trimpath -ldflags="-s -w" -o $(VASK)     ./cmd/vask
	go build -trimpath -ldflags="-s -w" -o $(BACKFILL) ./cmd/vask-embed-backfill
	go build -trimpath -ldflags="-s -w" -o $(WEB)      ./cmd/vask-web

build-arm64:
	mkdir -p $(BIN_DIR)
	GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o $(VASK).linux-arm64     ./cmd/vask
	GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o $(BACKFILL).linux-arm64 ./cmd/vask-embed-backfill
	GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o $(WEB).linux-arm64      ./cmd/vask-web

build-amd64:
	mkdir -p $(BIN_DIR)
	GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o $(VASK).linux-amd64     ./cmd/vask
	GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o $(BACKFILL).linux-amd64 ./cmd/vask-embed-backfill
	GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o $(WEB).linux-amd64      ./cmd/vask-web

tidy:
	go mod tidy

fmt:
	gofmt -s -w .

vet:
	go vet ./...

test:
	go test ./... -race

clean:
	rm -rf $(BIN_DIR) host_ed25519 host_ed25519.pub *.db *.db-journal *.db-wal *.db-shm
