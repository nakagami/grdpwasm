WASM_OUT      = static/main.wasm
PROXY_OUT     = proxy/proxy
WASM_EXEC_OUT = static/wasm_exec.js
GOROOT       := $(shell go env GOROOT)

WASM_SRCS  := $(wildcard *.go) go.mod go.sum
PROXY_SRCS := $(wildcard proxy/*.go) go.mod go.sum

.PHONY: all clean serve

$(WASM_EXEC_OUT):
	cp "$(GOROOT)/lib/wasm/wasm_exec.js" static/ 2>/dev/null || \
	cp "$(GOROOT)/misc/wasm/wasm_exec.js" static/

$(WASM_OUT): $(WASM_SRCS)
	GOOS=js GOARCH=wasm go build -o $(WASM_OUT) .

$(PROXY_OUT): $(PROXY_SRCS)
	go build -o $(PROXY_OUT) ./proxy

wasm: $(WASM_OUT)

proxy: $(PROXY_OUT)

wasm_exec: $(WASM_EXEC_OUT)

all: wasm_exec wasm proxy

clean:
	rm -f $(WASM_OUT) $(PROXY_OUT) $(WASM_EXEC_OUT)

serve: $(WASM_EXEC_OUT) $(WASM_OUT) $(PROXY_OUT)
	-lsof -ti :8080 | xargs kill -9 2>/dev/null; true
	./$(PROXY_OUT) -listen :8080 -static static
