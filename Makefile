ifeq ($(OS),Windows_NT)
    EXT := .exe
else
    EXT :=
endif

BIN_DIR := bin
APPS := host-webgl load-test

build: $(APPS)

$(APPS): %:
	go build -o $(BIN_DIR)/$@$(EXT) ./cmd/$@

# Кросс-платформенная сборка плагинов
build-plugin: $(addprefix plugin-,$(APPS))

plugin-%:
	go build -o $(BIN_DIR)/$*.bytes ./cmd/$*

.PHONY: build clean build-plugin plugin-%

clean:
	rm -f $(BIN_DIR)/*