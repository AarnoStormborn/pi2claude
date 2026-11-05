.PHONY: build test vet check install

build:
	go build -o bin/pi2claude .

test:
	go test ./...

vet:
	go vet ./...

check: vet test build

install:
	go install .

run:
	go run . $(ARGS)
