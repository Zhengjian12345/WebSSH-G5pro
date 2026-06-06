
.PHONY: clean all linux-arm64 WebSSH

all: linux-arm64

linux-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o WebSSH-linux-arm64

WebSSH:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o WebSSH

clean:
	rm -f WebSSH-linux-arm64 WebSSH
