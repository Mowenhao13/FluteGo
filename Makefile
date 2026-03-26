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
	GOOS=windows GOARCH=amd64 $(MAKE) release

darwin:
	GOOS=darwin GOARCH=amd64 $(MAKE) release
	GOOS=darwin GOARCH=arm64 $(MAKE) release

linux:
	GOOS=linux GOARCH=amd64 $(MAKE) release

clean:
	rm -rf $(DIST)
