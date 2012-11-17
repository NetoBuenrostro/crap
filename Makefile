default: fmt build

fmt:
	go fmt

build:
	go build

install: build
	sudo cp crap /usr/bin

uninstall:
	if [ -e "/usr/bin/crap" ]; then sudo rm /usr/bin/crap; fi
