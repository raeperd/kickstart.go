TARGET_EXEC := app
PORT := 8080
VERSION := local

default: clean build lint test 

download:
	go mod download

build: download
	go build -o $(TARGET_EXEC) -ldflags '-s -w -X main.Version=$(VERSION)' . 

test:
	go test -race -coverprofile=coverage.txt ./...

lint: download
	golangci-lint run

run: build
	./$(TARGET_EXEC) --port=$(PORT)

watch:
	air 

clean:
	rm -rf coverage.txt $(TARGET_EXEC) 
