BINARY := docstore
SCHEMA := schemas/schema1.json
TOKENS := token.json
DATA   := data/snapshot.json
PORT   := 8080

.PHONY: build run demo test clean

build:
	go build -o $(BINARY) .

run: build
	./$(BINARY) --schema $(SCHEMA) --tokens $(TOKENS) --data $(DATA) --port $(PORT)

demo: build
	./scripts/demo.sh

test:
	go test ./...

clean:
	rm -f $(BINARY)
