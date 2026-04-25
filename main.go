//go:build js && wasm

package main

import (
	"fmt"
	"image"
	"image/draw"
	"log/slog"
	"net"
	"sync"
	"syscall/js"

	"github.com/nakagami/grdp"
	"github.com/nakagami/grdp/plugin/rdpsnd"
)

var (
	rdpClient   *grdp.RdpClient
	clientMu    sync.Mutex
	screenImage *image.RGBA
	screenMu    sync.Mutex
	canvas      js.Value
	ctx2d       js.Value
)

func main() {
	js.Global().Set("rdpConnect", js.FuncOf(jsConnect))
	js.Global().Set("rdpDisconnect", js.FuncOf(jsDisconnect))
	js.Global().Set("rdpMouseMove", js.FuncOf(jsMouseMove))
	js.Global().Set("rdpMouseDown", js.FuncOf(jsMouseDown))
	js.Global().Set("rdpMouseUp", js.FuncOf(jsMouseUp))
	js.Global().Set("rdpMouseWheel", js.FuncOf(jsMouseWheel))
	js.Global().Set("rdpKeyDown", js.FuncOf(jsKeyDown))
	js.Global().Set("rdpKeyUp", js.FuncOf(jsKeyUp))

	// Block forever — JS callbacks keep things alive.
	select {}
}

// jsConnect is called from JS: rdpConnect(proxyWsURL, host, port, domain, user, password, width, height)
func jsConnect(_ js.Value, args []js.Value) any {
	if len(args) < 8 {
		return fmt.Sprintf("usage: rdpConnect(proxyWsURL, host, port, domain, user, password, width, height)")
	}
	proxyWsURL := args[0].String()
	host := args[1].String()
	port := args[2].String()
	domain := args[3].String()
	user := args[4].String()
	password := args[5].String()
	width := args[6].Int()
	height := args[7].Int()

	go func() {
		if err := connect(proxyWsURL, host, port, domain, user, password, width, height); err != nil {
			slog.Error("connect", "err", err)
			js.Global().Call("rdpOnError", err.Error())
		}
	}()
	return nil
}

func connect(proxyWsURL, host, port, domain, user, password string, width, height int) error {
	clientMu.Lock()
	if rdpClient != nil {
		rdpClient.Close()
		rdpClient = nil
	}
	clientMu.Unlock()

	hostPort := host + ":" + port

	// Build WebSocket URL for the proxy: ws://host:port/ws?target=rdphost:3389
	wsURL := proxyWsURL + "/ws?target=" + hostPort

	g := grdp.NewRdpClient(hostPort, width, height, func(hp string) (net.Conn, error) {
		return dialWebSocket(wsURL)
	})

	// Get canvas from DOM
	canvas = js.Global().Get("document").Call("getElementById", "rdpCanvas")
	ctx2d = canvas.Call("getContext", "2d")
	canvas.Set("width", width)
	canvas.Set("height", height)

	screenMu.Lock()
	screenImage = image.NewRGBA(image.Rect(0, 0, width, height))
	screenMu.Unlock()

	g.OnAudio(func(af rdpsnd.AudioFormat, data []byte) {
		cp := make([]byte, len(data))
		copy(cp, data)
		playAudio(int(af.SamplesPerSec), int(af.Channels), int(af.BitsPerSample), cp)
	})

	g.OnH264Raw(func(destX, destY, w, h int, isKey bool, data []byte) {
		jsArr := js.Global().Get("Uint8Array").New(len(data))
		js.CopyBytesToJS(jsArr, data)
		js.Global().Call("rdpOnH264", destX, destY, w, h, isKey, jsArr)
	})

	g.OnError(func(e error) {
		slog.Debug("rdp error", "err", e)
		js.Global().Call("rdpOnError", e.Error())
	}).OnClose(func() {
		slog.Debug("rdp close")
		js.Global().Call("rdpOnClose")
	}).OnSucces(func() {
		slog.Debug("rdp success")
	}).OnReady(func() {
		slog.Debug("rdp ready")
		js.Global().Call("rdpOnReady")
	}).OnBitmap(func(bs []grdp.Bitmap) {
		// Copy bitmap data before rendering (data is borrowed from pool)
		for i := range bs {
			d := make([]byte, len(bs[i].Data))
			copy(d, bs[i].Data)
			bs[i].Data = d
		}
		go renderBitmaps(bs)
	})

	if err := g.Login(domain, user, password); err != nil {
		return err
	}

	clientMu.Lock()
	rdpClient = g
	clientMu.Unlock()
	return nil
}

func renderBitmaps(bs []grdp.Bitmap) {
	screenMu.Lock()
	for _, bm := range bs {
		m := bm.RGBA()
		destRect := image.Rect(bm.DestLeft, bm.DestTop, bm.DestRight+1, bm.DestBottom+1)
		draw.Draw(screenImage, destRect, m, image.Pt(0, 0), draw.Src)
	}
	// Copy pixels from screenImage to a JS ImageData and draw on canvas
	pix := screenImage.Pix
	w := screenImage.Bounds().Dx()
	h := screenImage.Bounds().Dy()
	screenMu.Unlock()

	jsArr := js.Global().Get("Uint8ClampedArray").New(len(pix))
	js.CopyBytesToJS(jsArr, pix)
	imageData := js.Global().Get("ImageData").New(jsArr, w, h)
	ctx2d.Call("putImageData", imageData, 0, 0)
}

func jsDisconnect(_ js.Value, _ []js.Value) any {
	clientMu.Lock()
	defer clientMu.Unlock()
	if rdpClient != nil {
		rdpClient.Close()
		rdpClient = nil
	}
	return nil
}

func jsMouseMove(_ js.Value, args []js.Value) any {
	if len(args) < 2 {
		return nil
	}
	clientMu.Lock()
	c := rdpClient
	clientMu.Unlock()
	if c != nil {
		c.MouseMove(args[0].Int(), args[1].Int())
	}
	return nil
}

func jsMouseDown(_ js.Value, args []js.Value) any {
	if len(args) < 3 {
		return nil
	}
	clientMu.Lock()
	c := rdpClient
	clientMu.Unlock()
	if c != nil {
		c.MouseDown(args[0].Int(), args[1].Int(), args[2].Int())
	}
	return nil
}

func jsMouseUp(_ js.Value, args []js.Value) any {
	if len(args) < 3 {
		return nil
	}
	clientMu.Lock()
	c := rdpClient
	clientMu.Unlock()
	if c != nil {
		c.MouseUp(args[0].Int(), args[1].Int(), args[2].Int())
	}
	return nil
}

func jsMouseWheel(_ js.Value, args []js.Value) any {
	if len(args) < 1 {
		return nil
	}
	clientMu.Lock()
	c := rdpClient
	clientMu.Unlock()
	if c != nil {
		c.MouseWheel(args[0].Int())
	}
	return nil
}

func jsKeyDown(_ js.Value, args []js.Value) any {
	if len(args) < 1 {
		return nil
	}
	clientMu.Lock()
	c := rdpClient
	clientMu.Unlock()
	if c != nil {
		code := jsCodeToRDP(args[0].String())
		if code != 0 {
			c.KeyDown(code)
		}
	}
	return nil
}

func jsKeyUp(_ js.Value, args []js.Value) any {
	if len(args) < 1 {
		return nil
	}
	clientMu.Lock()
	c := rdpClient
	clientMu.Unlock()
	if c != nil {
		code := jsCodeToRDP(args[0].String())
		if code != 0 {
			c.KeyUp(code)
		}
	}
	return nil
}
