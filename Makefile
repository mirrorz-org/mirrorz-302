BIN ?= mirrorzd

.PHONY: all test clean $(BIN)

all: $(BIN)

test:
	go test ./...

clean:
	rm $(BIN)

$(BIN):
	go build -ldflags="-s -w" -o "$@"
