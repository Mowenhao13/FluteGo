DIST    := dist
LDFLAGS := -s -w

ifeq ($(OS),Windows_NT)
    EXT := .exe
else
    EXT :=
endif

.PHONY: all sender receiver clean

all: sender receiver

sender:
	go build -ldflags="$(LDFLAGS)" -o $(DIST)/flute_sender$(EXT) ./cmd/flute_sender/

receiver:
	go build -ldflags="$(LDFLAGS)" -o $(DIST)/flute_receiver$(EXT) ./cmd/flute_receiver/

clean:
	rm -rf $(DIST)
