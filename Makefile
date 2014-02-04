export GOPATH=$(shell pwd)

build:
	go build && go fmt

install: build
	sudo cp crap /usr/bin

lint:
	go get github.com/golang/lint/golint && bin/golint *.go

uninstall:
	if [ -e "/usr/bin/crap" ]; then sudo rm /usr/bin/crap; fi

clean:
	rm -rf pkg
