APP_NAME=fabric-agent
BUILD_DIR=bin
TAR_NAME=fabric-agent.tar.gz

.PHONY: all build run test clean package

all: build

build:
	@echo "Syncing dependencies..."
	go mod tidy
	@echo "Building $(APP_NAME)..."
	@mkdir -p $(BUILD_DIR)
	go build -o $(BUILD_DIR)/$(APP_NAME) main.go

run: build
	@./$(BUILD_DIR)/$(APP_NAME) --config config.yaml

test:
	@echo "Running unit tests..."
	go test ./...

clean:
	@echo "Cleaning workspace..."
	@rm -rf $(BUILD_DIR) tmp $(TAR_NAME)

package: clean
	@echo "Packaging project into $(TAR_NAME)..."
	@tar --exclude='*.tar.gz' --exclude='.git' -czvf $(TAR_NAME) .
	@echo "Created $(TAR_NAME)"
