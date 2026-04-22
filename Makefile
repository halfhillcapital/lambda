BIN    := bin
BINARY := $(BIN)/lambda
PKG    := ./cmd/lambda

.PHONY: build run test vet fmt tidy clean

build:
	@mkdir -p $(BIN)
	go build -o $(BINARY) $(PKG)

# Pass flags/prompts via ARGS, e.g. `make run ARGS="--model qwen2.5-coder -p hi"`.
run: build
	./$(BINARY) $(ARGS)

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -s -w .

tidy:
	go mod tidy

clean:
	rm -rf $(BIN)
