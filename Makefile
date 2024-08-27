TARGET_EXEC := app
PORT := 8080
IMAGE := ghcr.io/raeperd/go-http-template
TAG := local

all: compile test lint docker

compile:
	go build -o $(TARGET_EXEC) . 

test:
	go test -race ./...

lint:
	golangci-lint run

run: compile
	./$(TARGET_EXEC) --port=$(PORT)

docker:
	docker build . -t $(IMAGE):$(TAG)

docker-run: docker 
	docker run --rm -p $(PORT):8080 $(IMAGE):$(TAG)

clean:
	rm $(TARGET_EXEC)
	docker image rm $(IMAGE):$(TAG)
