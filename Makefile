WASM_OUT  = static/main.wasm
PROXY_OUT = proxy/proxy
GOROOT   := $(shell go env GOROOT)

.PHONY: all wasm proxy wasm_exec clean serve

all: wasm_exec wasm proxy

wasm:
	GOOS=js GOARCH=wasm go build -o $(WASM_OUT) .

proxy:
	go build -o $(PROXY_OUT) ./proxy

wasm_exec:
	cp "$(GOROOT)/lib/wasm/wasm_exec.js" static/ 2>/dev/null || \
	cp "$(GOROOT)/misc/wasm/wasm_exec.js" static/

clean:
	rm -f $(WASM_OUT) $(PROXY_OUT) static/wasm_exec.js

serve: all
	-fuser -k 8080/tcp 2>/dev/null || lsof -ti tcp:8080 | xargs kill 2>/dev/null; true
	./$(PROXY_OUT) -listen :8080 -static static
