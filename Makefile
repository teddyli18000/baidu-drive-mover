.PHONY: fmt vet test build

fmt:
	gofmt -w ./cmd ./internal

vet:
	go vet ./...

test:
	go test ./... -count=1

build:
	go build -trimpath -o bin/BaiduDriveMover.exe ./cmd/baidu-drive-mover
