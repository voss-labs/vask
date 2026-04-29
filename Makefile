.PHONY: run dev build build-arm64 build-amd64 tidy fmt vet test clean

BIN_DIR  := bin
ASK      := $(BIN_DIR)/ask
ASK_MOD  := $(BIN_DIR)/ask-mod

# local dev (runs on port 2300, ssh -p 2300 localhost)
run:
	go run ./cmd/ask

dev: run

# builds for the host architecture
build:
	mkdir -p $(BIN_DIR)
	go build -trimpath -ldflags="-s -w" -o $(ASK)     ./cmd/ask
	go build -trimpath -ldflags="-s -w" -o $(ASK_MOD) ./cmd/ask-mod

# Cross-compile for Oracle Cloud Always Free (Ampere ARM64)
build-arm64:
	mkdir -p $(BIN_DIR)
	GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o $(ASK).linux-arm64     ./cmd/ask
	GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o $(ASK_MOD).linux-arm64 ./cmd/ask-mod

# Cross-compile for Oracle Cloud Always Free AMD micro (VM.Standard.E2.1.Micro)
build-amd64:
	mkdir -p $(BIN_DIR)
	GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o $(ASK).linux-amd64     ./cmd/ask
	GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o $(ASK_MOD).linux-amd64 ./cmd/ask-mod

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
