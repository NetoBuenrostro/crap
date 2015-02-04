build:
	go build && go fmt

lint:
	golint *.go

clean:
	rm -rf pkg
