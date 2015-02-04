build: lint fmt
	go build

fmt:
	go fmt

lint:
	golint *.go
