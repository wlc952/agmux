.PHONY: build test clean install

build:
	go build -o bin/agmux ./cmd/agmux
	go build -o bin/agmux-server ./cmd/agmux-server

test:
	go test -cover ./...

clean:
	rm -rf bin/

install:
	cp bin/agmux /usr/local/bin/agmux
	cp bin/agmux-server /usr/local/bin/agmux-server