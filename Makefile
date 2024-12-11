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
DOCKER_VERSION := $(if $(VERSION),$(subst /,-,$(VERSION)),latest)

docker:
	docker build . --build-arg VERSION=$(VERSION) -t $(IMAGE):$(DOCKER_VERSION)

docker-run: docker 
	docker run --rm -p $(PORT):8080 $(IMAGE):$(DOCKER_VERSION)

docker-clean:
	docker image rm -f $(IMAGE):$(DOCKER_VERSION) || true
