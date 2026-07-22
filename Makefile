BINARY := btreedb
SCHEMA := schemas/document.json
DATA   := data/snapshot.json
PORT   := 8080

.PHONY: build run demo test clean

build:
	go build -o $(BINARY) .

run: build
	./$(BINARY) --schema $(SCHEMA) --data $(DATA) --port $(PORT)

demo: build
	./scripts/demo.sh

test:
	go test ./...

clean:
	rm -f $(BINARY)
