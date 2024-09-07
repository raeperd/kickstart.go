TARGET_EXEC := app
PORT := 8080
IMAGE := ghcr.io/raeperd/kickstart
TAG := local
VERSION ?= $(TAG)

all: build test lint docker

build:
	go build -o $(TARGET_EXEC) -ldflags '-X main.Version=$(VERSION)' . 

test:
	go test -race ./...

lint:
	golangci-lint run

run: compile
	./$(TARGET_EXEC) --port=$(PORT)

docker:
	docker build . --build-arg VERSION=$(VERSION) -t $(IMAGE):$(TAG)

docker-run: docker 
	docker run --rm -p $(PORT):8080 $(IMAGE):$(TAG)

clean:
	rm $(TARGET_EXEC)
	docker image rm $(IMAGE):$(TAG)
