BIN ?= mirrorzd

.PHONY: all clean $(BIN)

all: $(BIN)

clean:
	rm $(BIN)

$(BIN):
	go build -o "$@"
