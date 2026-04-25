//go:build js && wasm

package main

import (
	"fmt"
	"net"
	"syscall/js"
	"time"
)

// wsConn implements net.Conn over a browser WebSocket.
// It is created by dialWebSocket and injected into grdp via RdpClient.Dialer.
type wsConn struct {
	ws      js.Value
	readBuf chan []byte
	pending []byte
	closed  bool
}

func dialWebSocket(proxyURL string) (net.Conn, error) {
	c := &wsConn{
		readBuf: make(chan []byte, 256),
	}

	ws := js.Global().Get("WebSocket").New(proxyURL)
	ws.Set("binaryType", "arraybuffer")
	c.ws = ws

	openCh := make(chan error, 1)

	var onOpen, onError, onMessage, onClose js.Func

	onOpen = js.FuncOf(func(this js.Value, args []js.Value) any {
		openCh <- nil
		return nil
	})
	onError = js.FuncOf(func(this js.Value, args []js.Value) any {
		select {
		case openCh <- fmt.Errorf("websocket error"):
		default:
		}
		return nil
	})
	onMessage = js.FuncOf(func(this js.Value, args []js.Value) any {
		data := args[0].Get("data")
		arr := js.Global().Get("Uint8Array").New(data)
		buf := make([]byte, arr.Length())
		js.CopyBytesToGo(buf, arr)
		if !c.closed {
			select {
			case c.readBuf <- buf:
			default:
				// drop if buffer full
			}
		}
		return nil
	})
	onClose = js.FuncOf(func(this js.Value, args []js.Value) any {
		c.closed = true
		close(c.readBuf)
		return nil
	})

	ws.Set("onopen", onOpen)
	ws.Set("onerror", onError)
	ws.Set("onmessage", onMessage)
	ws.Set("onclose", onClose)

	err := <-openCh
	// Release open/error handlers; keep message/close handlers alive via c.ws
	onOpen.Release()
	onError.Release()
	// Store message and close handlers so they are not GC'd
	c.ws.Set("_onmessage", onMessage)
	c.ws.Set("_onclose", onClose)

	if err != nil {
		return nil, err
	}
	return c, nil
}

func (c *wsConn) Read(b []byte) (int, error) {
	// Drain pending bytes from last read first.
	if len(c.pending) > 0 {
		n := copy(b, c.pending)
		c.pending = c.pending[n:]
		return n, nil
	}

	chunk, ok := <-c.readBuf
	if !ok {
		return 0, fmt.Errorf("websocket closed")
	}
	n := copy(b, chunk)
	if n < len(chunk) {
		c.pending = chunk[n:]
	}
	return n, nil
}

func (c *wsConn) Write(b []byte) (int, error) {
	if c.closed {
		return 0, fmt.Errorf("websocket closed")
	}
	arr := js.Global().Get("Uint8Array").New(len(b))
	js.CopyBytesToJS(arr, b)
	c.ws.Call("send", arr.Get("buffer"))
	return len(b), nil
}

func (c *wsConn) Close() error {
	if !c.closed {
		c.closed = true
		c.ws.Call("close")
	}
	return nil
}

func (c *wsConn) LocalAddr() net.Addr                { return wsAddr("local") }
func (c *wsConn) RemoteAddr() net.Addr               { return wsAddr("remote") }
func (c *wsConn) SetDeadline(t time.Time) error      { return nil }
func (c *wsConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *wsConn) SetWriteDeadline(t time.Time) error { return nil }

type wsAddr string

func (a wsAddr) Network() string { return "websocket" }
func (a wsAddr) String() string  { return string(a) }
