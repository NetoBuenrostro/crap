build: lint fmt compile

compile:
	@go build

fmt:
	go fmt

lint:
	golint *.go

install: compile
	sudo cp crap /usr/local/bin

uninstall:
	if [ -e "/usr/local/bin/crap" ]; then sudo rm /usr/local/bin/crap; fi