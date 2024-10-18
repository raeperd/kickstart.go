TARGET_EXEC := app
PORT := 8080
VERSION := local

default: clean build lint test 

download:
	go mod download

build: download
	go build -o $(TARGET_EXEC) -ldflags '-w -X main.Version=$(VERSION)' . 

test:
	go test -shuffle=on -race -coverprofile=coverage.txt ./...

lint: download
	golangci-lint run

run: build
	./$(TARGET_EXEC) --port=$(PORT)

watch:
	air 

clean:
	rm -rf coverage.txt $(TARGET_EXEC) 

IMAGE := ghcr.io/raeperd/kickstart
docker:
	docker build . --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION)

docker-run: docker 
	docker run --rm -p $(PORT):8080 $(IMAGE):$(VERSION)

docker-clean:
	docker image rm -f $(IMAGE):$(VERSION) || true
