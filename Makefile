build:
	go build && go fmt && go vet

install: build
	sudo cp crap /usr/bin

uninstall:
	if [ -e "/usr/bin/crap" ]; then sudo rm /usr/bin/crap; fi
