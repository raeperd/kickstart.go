TARGET_EXEC := app

all: compile test lint 

compile:
	go build -o $(TARGET_EXEC) . 

test:
	go test ./...

lint:
	golangci-lint run

run: PORT := 8080
run: compile
	./$(TARGET_EXEC) --port=$(PORT)

clean:
	rm $(TARGET_EXEC)
