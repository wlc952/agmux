.PHONY: build test clean install

build:
	go build -o bin/gssh ./cmd/gssh

test:
	go test -cover ./...

clean:
	rm -rf bin/

install:
	cp bin/gssh /usr/local/bin/gssh
