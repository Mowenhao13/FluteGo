DIST    := dist
LDFLAGS := -s -w

# Go 编译优化参数
GOFLAGS := -trimpath
CGO_ENABLED := 0

ifeq ($(OS),Windows_NT)
    EXT := .exe
else
    EXT :=
endif

.PHONY: all sender receiver clean windows darwin linux

all: sender receiver

# 快速编译（开发用，不 strip）
debug: LDFLAGS :=
debug: all

# 优化编译（发布用）
release: sender receiver

sender:
	CGO_ENABLED=$(CGO_ENABLED) go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(DIST)/flute_sender$(EXT) ./cmd/flute_sender/

receiver:
	CGO_ENABLED=$(CGO_ENABLED) go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(DIST)/flute_receiver$(EXT) ./cmd/flute_receiver/

# 平台特定编译
windows:
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(DIST)/flute_sender.exe ./cmd/flute_sender/
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(DIST)/flute_receiver.exe ./cmd/flute_receiver/

darwin:
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(DIST)/flute_sender_darwin_amd64 ./cmd/flute_sender/
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(DIST)/flute_receiver_darwin_amd64 ./cmd/flute_receiver/
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(DIST)/flute_sender_darwin_arm64 ./cmd/flute_sender/
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(DIST)/flute_receiver_darwin_arm64 ./cmd/flute_receiver/

linux:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(DIST)/flute_sender_linux_amd64 ./cmd/flute_sender/
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(DIST)/flute_receiver_linux_amd64 ./cmd/flute_receiver/

clean:
	rm -rf $(DIST)
