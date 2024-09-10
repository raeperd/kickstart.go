TARGET_EXEC := app
PORT := 8080
IMAGE := ghcr.io/raeperd/kickstart
TAG := local
VERSION ?= $(TAG)

all: build test lint docker

download:
	go mod download

build: download
	go build -o $(TARGET_EXEC) -ldflags '-X main.Version=$(VERSION)' . 

test:
	go test -race ./...

lint: download
	golangci-lint run

run: build
	./$(TARGET_EXEC) --port=$(PORT)

watch:
	air 

docker:
	docker build . --build-arg VERSION=$(VERSION) -t $(IMAGE):$(TAG)

docker-run: docker 
	docker run --rm -p $(PORT):8080 $(IMAGE):$(TAG)

clean:
	rm $(TARGET_EXEC)
	docker image rm $(IMAGE):$(TAG)
