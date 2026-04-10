TARGET_EXEC := app
PORT := 8080
VERSION := local

default: clean build lint test 

tidy:
	go mod tidy

build: tidy
	go build -o $(TARGET_EXEC) -ldflags '-w -X main.Version=$(VERSION)' . 

test:
	go test -shuffle=on -race -coverprofile=coverage.txt ./...

lint: tidy
	golangci-lint run
	go run golang.org/x/tools/cmd/deadcode@latest -test ./...

run: build
	PORT=$(PORT) ./$(TARGET_EXEC)

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
