build:
	go build && go fmt && go vet

install: build
	sudo cp crap /usr/bin

lint:
	go get github.com/golang/lint/golint && bin/golint *.go

uninstall:
	if [ -e "/usr/bin/crap" ]; then sudo rm /usr/bin/crap; fi
