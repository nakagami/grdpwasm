package grdp

import (
	"fmt"
	"image"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/nakagami/grdp/plugin"
	"github.com/nakagami/grdp/plugin/cliprdr"
	"github.com/nakagami/grdp/plugin/drdynvc"
	"github.com/nakagami/grdp/plugin/rdpgfx"
	"github.com/nakagami/grdp/plugin/rdpsnd"

	"github.com/nakagami/grdp/core"
	"github.com/nakagami/grdp/protocol/nla"
	"github.com/nakagami/grdp/protocol/pdu"
	"github.com/nakagami/grdp/protocol/sec"
	"github.com/nakagami/grdp/protocol/t125"
	"github.com/nakagami/grdp/protocol/t125/gcc"
	"github.com/nakagami/grdp/protocol/tpkt"
	"github.com/nakagami/grdp/protocol/x224"
)

// stubChannel is a no-op virtual channel handler for channels the server
// expects to be present (e.g. rdpdr, cliprdr) but that we don't process.
type stubChannel struct {
	name   string
	option uint32
	sender core.ChannelSender
}

func (s *stubChannel) GetType() (string, uint32)   { return s.name, s.option }
func (s *stubChannel) Sender(f core.ChannelSender) { s.sender = f }
func (s *stubChannel) Process(data []byte)         {}

type RdpClient struct {
	hostPort        string // ip:port
	width           int
	height          int
	kbdLayout       uint32
	keyboardType    uint32
	keyboardSubType uint32
	tpkt            *tpkt.TPKT
	x224            *x224.X224
	mcs             *t125.MCSClient
	sec             *sec.Client
	pdu             *pdu.Client
	channels        *plugin.Channels
	eventReady      bool
	redirecting     bool      // true during async redirect reconnection
	decompressPool  sync.Pool // pools []uint8 buffers for bitmap decompression
	flipLinePool    sync.Pool // pools line-sized []uint8 buffers for bitmap vertical flip
	closed          bool

	// credentials stored for reconnection
	domain   string
	user     string
	password string

	// stored callbacks for re-registration on reconnect
	onErrorFn         func(e error)
	onCloseFn         func()
	onSuccesFn        func()
	onReadyFn         func()
	onBitmapPaintFn   func([]Bitmap)
	onPointerHideFn   func()
	onPointerCachedFn func(uint16)
	onPointerUpdateFn func(uint16, uint16, uint16, uint16, uint16, uint16, []byte, []byte)
	onAudioFn         func(rdpsnd.AudioFormat, []byte)
	onAudioResetFn    func()
	onH264RawFn       func(destX, destY, w, h int, isKey bool, data []byte)

	// clipboard callbacks and handler
	onClipboardFn  func(text string) // remote → local
	getClipboardFn func() string     // local → remote
	cliprdrHandler *cliprdr.CliprdrHandler

	// Mouse-move coalescing.  High-frequency UI move events (often one per
	// host pixel) are collapsed into at most one network PDU per
	// mouseCoalesceInterval, with the latest position always winning.
	// Button/key events are sent immediately but flush any pending move
	// first so server-side ordering is preserved.
	mouseMu      sync.Mutex
	mousePending bool
	mouseX       int
	mouseY       int
	mouseTimer   *time.Timer
	mouseLastTx  time.Time

	dialer func(hostPort string) (net.Conn, error)
}

const mouseCoalesceInterval = 16 * time.Millisecond

// Bitmap is a single rendered region delivered to the OnBitmap callback.
//
// Lifecycle: Bitmap.Data is borrowed from an internal buffer pool and is
// only valid for the duration of the synchronous OnBitmap callback.  After
// the callback returns the slice may be returned to the pool and overwritten
// by subsequent updates.  Callers that need to retain the pixels (e.g. to
// hand them to an asynchronous paint goroutine) MUST copy the bytes before
// the callback returns.
type Bitmap struct {
	DestLeft     int
	DestTop      int
	DestRight    int
	DestBottom   int
	Width        int
	Height       int
	BitsPerPixel int
	Data         []byte
}

func (bm *Bitmap) RGBA() *image.RGBA {
	m := image.NewRGBA(image.Rect(0, 0, bm.Width, bm.Height))
	pix := m.Pix
	data := bm.Data

	// Per-format specialised loops avoid a per-pixel switch and let the
	// compiler hoist bounds checks and emit tight, branch-free inner code.
	switch bm.BitsPerPixel {
	case 1:
		// 16-bit RGB555 stored big-endian in two bytes.
		n := len(pix) >> 2
		if len(data) < n*2 {
			n = len(data) / 2
		}
		for i := 0; i < n; i++ {
			d := uint16(data[i*2])<<8 | uint16(data[i*2+1])
			pix[i*4] = uint8((d & 0x7C00) >> 7)
			pix[i*4+1] = uint8((d & 0x03E0) >> 2)
			pix[i*4+2] = uint8((d & 0x001F) << 3)
			pix[i*4+3] = 0xFF
		}
	case 2:
		// 16-bit RGB565 stored big-endian in two bytes.
		n := len(pix) >> 2
		if len(data) < n*2 {
			n = len(data) / 2
		}
		for i := 0; i < n; i++ {
			d := uint16(data[i*2])<<8 | uint16(data[i*2+1])
			pix[i*4] = uint8((d & 0xF800) >> 8)
			pix[i*4+1] = uint8((d & 0x07E0) >> 3)
			pix[i*4+2] = uint8((d & 0x001F) << 3)
			pix[i*4+3] = 0xFF
		}
	default:
		// 24/32-bit BGR(A) → RGBA with stride = bm.BitsPerPixel.
		stride := bm.BitsPerPixel
		n := len(pix) >> 2
		if len(data) < n*stride {
			n = len(data) / stride
		}
		for i := 0; i < n; i++ {
			s := i * stride
			pix[i*4] = data[s+2]
			pix[i*4+1] = data[s+1]
			pix[i*4+2] = data[s]
			pix[i*4+3] = 0xFF
		}
	}
	return m
}

func NewRdpClient(host string, width, height int, dialer func(string) (net.Conn, error)) *RdpClient {
	return &RdpClient{
		hostPort:        host,
		width:           width,
		height:          height,
		kbdLayout:       uint32(gcc.US),
		keyboardType:    uint32(gcc.KT_IBM_101_102_KEYS),
		keyboardSubType: 0,
		dialer:          dialer,
		decompressPool: sync.Pool{
			New: func() any { return []uint8(nil) },
		},
		flipLinePool: sync.Pool{
			New: func() any { return []uint8(nil) },
		},
	}
}

var keyboardLayoutMap = map[string]uint32{
	"ARABIC":              uint32(gcc.ARABIC),
	"BULGARIAN":           uint32(gcc.BULGARIAN),
	"CHINESE_US_KEYBOARD": uint32(gcc.CHINESE_US_KEYBOARD),
	"CZECH":               uint32(gcc.CZECH),
	"DANISH":              uint32(gcc.DANISH),
	"GERMAN":              uint32(gcc.GERMAN),
	"GREEK":               uint32(gcc.GREEK),
	"US":                  uint32(gcc.US),
	"SPANISH":             uint32(gcc.SPANISH),
	"FINNISH":             uint32(gcc.FINNISH),
	"FRENCH":              uint32(gcc.FRENCH),
	"HEBREW":              uint32(gcc.HEBREW),
	"HUNGARIAN":           uint32(gcc.HUNGARIAN),
	"ICELANDIC":           uint32(gcc.ICELANDIC),
	"ITALIAN":             uint32(gcc.ITALIAN),
	"JAPANESE":            uint32(gcc.JAPANESE),
	"KOREAN":              uint32(gcc.KOREAN),
	"DUTCH":               uint32(gcc.DUTCH),
	"NORWEGIAN":           uint32(gcc.NORWEGIAN),
}

var keyboardTypeMap = map[string]uint32{
	"IBM_PC_XT_83_KEY": uint32(gcc.KT_IBM_PC_XT_83_KEY),
	"OLIVETTI":         uint32(gcc.KT_OLIVETTI),
	"IBM_PC_AT_84_KEY": uint32(gcc.KT_IBM_PC_AT_84_KEY),
	"IBM_101_102_KEYS": uint32(gcc.KT_IBM_101_102_KEYS),
	"NOKIA_1050":       uint32(gcc.KT_NOKIA_1050),
	"NOKIA_9140":       uint32(gcc.KT_NOKIA_9140),
	"JAPANESE":         uint32(gcc.KT_JAPANESE),
}

// SetKeyboardLayout sets the keyboard layout by name (e.g. "US", "FRENCH").
// Must be called before Login.
func (g *RdpClient) SetKeyboardLayout(layout string) {
	if v, ok := keyboardLayoutMap[strings.ToUpper(layout)]; ok {
		g.kbdLayout = v
	} else {
		slog.Warn("Unknown keyboard layout, falling back to US", "layout", layout)
		g.kbdLayout = uint32(gcc.US)
	}
}

// SetKeyboardType sets the keyboard type by name (e.g. "IBM_101_102_KEYS").
// Must be called before Login.
func (g *RdpClient) SetKeyboardType(keyboardType string) {
	if v, ok := keyboardTypeMap[strings.ToUpper(keyboardType)]; ok {
		g.keyboardType = v
	} else {
		slog.Warn("Unknown keyboard type, falling back to IBM_101_102_KEYS", "keyboardType", keyboardType)
		g.keyboardType = uint32(gcc.KT_IBM_101_102_KEYS)
	}
}

func bpp(BitsPerPixel uint16) (pixel int) {
	switch BitsPerPixel {
	case 15:
		pixel = 2

	case 16:
		pixel = 2

	case 24:
		pixel = 3

	case 32:
		pixel = 4

	default:
		slog.Error("invalid bitmap data format")
	}
	return
}

func (g *RdpClient) Login(domain string, user string, password string) error {
	slog.Debug("Login", "Host", g.hostPort, "domain", domain, "user", user)

	g.domain = domain
	g.user = user
	g.password = password

	return g.doLogin(nil)
}

// doLogin establishes an RDP connection.
// When routingToken is non-nil it replaces the username cookie in the
// x224 Connection Request (required for Server Redirection).
func (g *RdpClient) doLogin(routingToken []byte) error {
	conn, err := g.dialer(g.hostPort)
	if err != nil {
		return fmt.Errorf("[dial err] %v", err)
	}

	host, _, _ := net.SplitHostPort(g.hostPort)
	g.tpkt = tpkt.New(core.NewSocketLayer(conn, host), nla.NewNTLMv2(g.domain, g.user, g.password))
	g.x224 = x224.New(g.tpkt)
	g.mcs = t125.NewMCSClient(g.x224, g.kbdLayout, g.keyboardType, g.keyboardSubType)
	g.sec = sec.NewClient(g.mcs)
	g.pdu = pdu.NewClient(g.sec)
	g.channels = plugin.NewChannels(g.sec)

	// Wire user-registered callbacks now that g.pdu is initialised.
	// This allows callers to invoke On* methods before Login.
	g.reregisterCallbacks()

	// Wire RemoteFX surface decoder so the pdu layer can decode
	// codecID=3 in surface bitmap commands without importing rdpgfx.
	pdu.DecodeRemoteFX = rdpgfx.DecodeSurfaceRFX

	g.mcs.SetClientDesktop(uint16(g.width), uint16(g.height))

	// Register channels in order: rdpdr, rdpsnd, cliprdr, drdynvc
	// (matching the channel order that Windows servers expect)

	// rdpdr (Device Redirection) — stub, required for server to enable audio
	g.channels.Register(&stubChannel{name: "rdpdr",
		option: plugin.CHANNEL_OPTION_INITIALIZED | plugin.CHANNEL_OPTION_ENCRYPT_RDP | plugin.CHANNEL_OPTION_COMPRESS_RDP})
	g.mcs.SetClientDeviceRedirection()

	// RDPSND (Audio Output) handler — static virtual channel + DVC paths
	rdpsndHandler := rdpsnd.NewHandler(func(format rdpsnd.AudioFormat, data []byte) {
		if g.onAudioFn != nil {
			g.onAudioFn(format, data)
		}
	})
	rdpsndHandler.SetAudioResetCallback(func() {
		if g.onAudioResetFn != nil {
			g.onAudioResetFn()
		}
	})
	g.channels.Register(rdpsndHandler)
	g.mcs.SetClientSoundProtocol()

	// cliprdr (Clipboard) — cross-platform text clipboard handler
	cliprdrHandler := cliprdr.NewHandler(
		func(text string) {
			if g.onClipboardFn != nil {
				g.onClipboardFn(text)
			}
		},
		func() string {
			if g.getClipboardFn != nil {
				return g.getClipboardFn()
			}
			return ""
		},
	)
	g.cliprdrHandler = cliprdrHandler
	g.channels.Register(cliprdrHandler)
	g.mcs.SetClientClipboard()

	// drdynvc (Dynamic Virtual Channels)
	dvcClient := drdynvc.NewDvcClient()
	g.channels.Register(dvcClient)
	g.mcs.SetClientDynvcProtocol()

	// RDPGFX (Graphics Pipeline) handler
	gfxHandler := rdpgfx.NewGfxHandler(func(updates []rdpgfx.BitmapUpdate) {
		if g.onBitmapPaintFn == nil {
			return
		}
		bs := make([]Bitmap, len(updates))
		for i, u := range updates {
			bs[i] = Bitmap{
				DestLeft:     u.DestLeft,
				DestTop:      u.DestTop,
				DestRight:    u.DestRight,
				DestBottom:   u.DestBottom,
				Width:        u.Width,
				Height:       u.Height,
				BitsPerPixel: u.Bpp,
				Data:         u.Data,
			}
		}
		g.onBitmapPaintFn(bs)
	})
	gfxHandler.SetKeyframeNeededCallback(func(force bool) {
		if force {
			g.pdu.SendForceRefresh(uint16(g.width), uint16(g.height))
		} else {
			g.pdu.SendRefreshRect(uint16(g.width), uint16(g.height))
		}
	})
	gfxHandler.SetDecoderBrokenCallback(func() {
		slog.Warn("H.264 decoder broken; reconnecting RDP session")
		go func() {
			if err := g.Reconnect(g.width, g.height); err != nil {
				slog.Warn("Reconnect after decoder broken failed", "err", err)
			}
		}()
	})
	if g.onH264RawFn != nil {
		gfxHandler.SetH264RawCallback(g.onH264RawFn)
	}
	dvcClient.RegisterHandler(rdpgfx.ChannelName, gfxHandler)

	// Reject Video Optimized Remoting (VOR) channels so the server keeps
	// sending video through the RDPGFX pipeline which we do handle.
	// Without this, the server detects video playback (e.g. YouTube) and
	// switches to VOR channels that we don't implement, causing the video
	// to freeze while audio continues.
	dvcClient.RegisterRejectedChannel("Microsoft::Windows::RDS::Video::Control::v08.01")
	dvcClient.RegisterRejectedChannel("Microsoft::Windows::RDS::Video::Data::v08.01")
	dvcClient.RegisterRejectedChannel("Microsoft::Windows::RDS::Geometry::v08.01")

	// Register DVC audio handlers for both the lossless and lossy variants.
	// gnome-remote-desktop requests AUDIO_PLAYBACK_LOSSY_DVC first; if it is
	// rejected, gnome-remote-desktop triggers its SVC fallback path which also
	// sets prevent_dvc_initialization=true, silently blocking AUDIO_PLAYBACK_DVC
	// as well — leaving the client with no audio at all.
	// By accepting both channels with the same rdpsnd handler, format negotiation
	// (which only advertises PCM) ensures PCM is used regardless of which channel
	// gnome-remote-desktop chooses.
	dvcClient.RegisterHandler("AUDIO_PLAYBACK_DVC", rdpsnd.NewDvcAdapter(rdpsndHandler))
	dvcClient.RegisterHandler("AUDIO_PLAYBACK_LOSSY_DVC", rdpsnd.NewDvcAdapter(rdpsndHandler))

	g.sec.SetUser(g.user)
	g.sec.SetPwd(g.password)
	g.sec.SetDomain(g.domain)

	g.tpkt.SetFastPathListener(g.sec)
	g.sec.SetFastPathListener(g.pdu)
	g.sec.SetChannelSender(g.mcs)
	g.channels.SetChannelSender(g.sec)

	// Wire fast-path output: pdu → sec → tpkt.  This enables the much
	// shorter Fast-Path Client Input PDU framing for mouse/keyboard events
	// (MS-RDPBCGR §2.2.8.1.2).  Use is gated at runtime both by capability
	// negotiation in the PDU layer and by sec.SendFastPath itself, which
	// refuses when legacy RDP encryption is in effect.
	g.sec.SetFastPathSender(g.tpkt)
	g.pdu.SetFastPathSender(g.sec)

	g.x224.SetRequestedProtocol(x224.PROTOCOL_SSL | x224.PROTOCOL_HYBRID)
	if routingToken != nil {
		g.x224.SetRoutingToken(routingToken)
	} else {
		g.x224.SetUsername(g.user)
	}

	err = g.x224.Connect()
	if err != nil {
		return fmt.Errorf("[x224 connect err] %v", err)
	}

	// Wait for the RDP handshake to complete or fail.
	// Events arrive asynchronously from the TPKT read goroutine.
	type connResult struct {
		err      error
		redirect *pdu.ServerRedirectionPDU
		retry    bool // deactivateAll → need GFX retry
	}

	ch := make(chan connResult, 4)
	send := func(r connResult) {
		select {
		case ch <- r:
		default:
		}
	}

	// readyFired is set by the "ready" callback. All emitter callbacks
	// run synchronously on the TPKT read goroutine, so no mutex needed.
	readyFired := false

	g.pdu.On("ready", func() {
		g.eventReady = true
		readyFired = true
		send(connResult{})
	})

	g.pdu.On("error", func(err error) {
		if !readyFired {
			send(connResult{err: err})
		}
	})

	// Redirect may arrive before or after "ready".
	// Before ready: send to channel for synchronous handling.
	// After ready: launch async goroutine (GNOME Remote Desktop
	// sends redirect ~5s after the GFX retry's "ready").
	g.pdu.Once("redirect", func(redir *pdu.ServerRedirectionPDU) {
		if !readyFired {
			send(connResult{redirect: redir})
		} else {
			go g.handleRedirect(redir)
		}
	})

	select {
	case r := <-ch:
		if r.err != nil {
			g.tpkt.Close()
			return fmt.Errorf("[connection err] %v", r.err)
		}
		if r.retry {
			slog.Debug("Server requires GFX, retrying with GFX flag")
			g.tpkt.Close()
			g.eventReady = false
			time.Sleep(2 * time.Second)
			return g.doLogin(nil)
		}
		if r.redirect != nil {
			slog.Debug("Server redirect", "loadBalanceInfo", string(r.redirect.LoadBalanceInfo))
			g.tpkt.Close()
			g.eventReady = false
			return g.doLogin(r.redirect.LoadBalanceInfo)
		}
		// "ready" received — session established.
		return nil
	case <-time.After(30 * time.Second):
		g.tpkt.Close()
		return fmt.Errorf("[connection timeout]")
	}
}

// handleRedirect handles a Server Redirection PDU that arrives after
// "ready" (e.g. GNOME Remote Desktop). Runs asynchronously.
func (g *RdpClient) handleRedirect(redir *pdu.ServerRedirectionPDU) {
	slog.Debug("Async server redirect", "loadBalanceInfo", string(redir.LoadBalanceInfo))
	g.redirecting = true
	g.tpkt.Close()
	g.eventReady = false

	err := g.doLogin(redir.LoadBalanceInfo)
	g.redirecting = false
	if err != nil {
		slog.Error("handleRedirect: login failed", "err", err)
		if g.onErrorFn != nil {
			g.onErrorFn(err)
		}
		return
	}
	g.reregisterCallbacks()
}

func (g *RdpClient) Width() int {
	return g.width
}

func (g *RdpClient) Height() int {
	return g.height
}

func (g *RdpClient) OnError(f func(e error)) *RdpClient {
	g.onErrorFn = f
	if g.pdu != nil {
		g.pdu.On("error", func(e error) {
			if !g.redirecting {
				f(e)
			}
		})
	}
	return g
}

func (g *RdpClient) OnClose(f func()) *RdpClient {
	g.onCloseFn = f
	if g.pdu != nil {
		g.pdu.On("close", f)
	}
	return g
}

func (g *RdpClient) OnSucces(f func()) *RdpClient {
	g.onSuccesFn = f
	if g.pdu != nil {
		g.pdu.On("succes", f)
	}
	return g
}

func (g *RdpClient) OnReady(f func()) *RdpClient {
	g.onReadyFn = f
	if g.pdu != nil {
		g.pdu.On("ready", f)
	}
	return g
}

// OnBitmap registers a callback for bitmap update events.
// For compressed bitmaps, Bitmap.Data is borrowed from an internal pool and
// is valid only for the duration of the paint call. If you need to retain
// the raw pixel data beyond paint, copy it or call bm.RGBA() inside paint.
func (g *RdpClient) OnBitmap(paint func([]Bitmap)) *RdpClient {
	g.onBitmapPaintFn = paint
	if g.pdu == nil {
		return g
	}
	g.pdu.On("bitmap", func(rectangles []pdu.BitmapData) {
		bs := make([]Bitmap, 0, len(rectangles))
		var pooled [][]uint8 // track buffers borrowed from pool

		for _, v := range rectangles {
			data := v.BitmapDataStream
			Bpp := bpp(v.BitsPerPixel)

			if v.Flags&pdu.BITMAP_NO_PROCESSING != 0 {
				// Surface command: data is already decoded top-down BGRA
			} else if v.IsCompress() {
				buf := g.decompressPool.Get().([]uint8)
				buf = core.DecompressInto(v.BitmapDataStream, buf, int(v.Width), int(v.Height), Bpp)
				data = buf
				pooled = append(pooled, buf)
			} else {
				// Uncompressed bitmaps are bottom-up; flip to top-down.
				stride := int(v.Width) * Bpp
				h := int(v.Height)
				tmp := g.flipLinePool.Get().([]byte)
				if cap(tmp) < stride {
					tmp = make([]byte, stride)
				} else {
					tmp = tmp[:stride]
				}
				for y := 0; y < h/2; y++ {
					top := y * stride
					bot := (h - 1 - y) * stride
					copy(tmp, data[top:top+stride])
					copy(data[top:top+stride], data[bot:bot+stride])
					copy(data[bot:bot+stride], tmp)
				}
				g.flipLinePool.Put(tmp[:cap(tmp)])
			}

			b := Bitmap{int(v.DestLeft), int(v.DestTop), int(v.DestRight), int(v.DestBottom),
				int(v.Width), int(v.Height), Bpp, data}
			bs = append(bs, b)
		}
		paint(bs)

		for _, buf := range pooled {
			g.decompressPool.Put(buf[:cap(buf)])
		}
	})
	return g
}

// OnH264Raw registers a callback that receives raw H.264 NAL unit data when
// the built-in decoder is unavailable (e.g. WASM builds without CGo).
// destX, destY are the top-left canvas coordinates; isKey flags an IDR frame.
// The caller owns data and may retain it beyond the callback.
func (g *RdpClient) OnH264Raw(fn func(destX, destY, w, h int, isKey bool, data []byte)) *RdpClient {
	g.onH264RawFn = fn
	return g
}

func (g *RdpClient) OnPointerHide(f func()) *RdpClient {
	g.onPointerHideFn = f
	if g.pdu != nil {
		g.pdu.On("pointer_hide", f)
	}
	return g
}

func (g *RdpClient) OnPointerCached(f func(uint16)) *RdpClient {
	g.onPointerCachedFn = f
	if g.pdu != nil {
		g.pdu.On("pointer_cached", f)
	}
	return g
}

func (g *RdpClient) OnPointerUpdate(f func(uint16, uint16, uint16, uint16, uint16, uint16, []byte, []byte)) *RdpClient {
	g.onPointerUpdateFn = f
	if g.pdu != nil {
		g.pdu.On("pointer_update", func(p *pdu.FastPathUpdatePointerPDU) {
			f(p.CacheIdx, p.XorBpp, p.X, p.Y, p.Width, p.Height, p.Mask, p.Data)
		})
	}
	return g
}

// OnAudio registers a callback for server audio data.
// The callback receives the AudioFormat describing the PCM data and the raw audio bytes.
// Must be called before Login.
func (g *RdpClient) OnAudio(f func(rdpsnd.AudioFormat, []byte)) *RdpClient {
	g.onAudioFn = f
	return g
}

// OnAudioReset registers a callback that is called when the server closes the
// audio channel (e.g. media seek or stream restart). The application should
// flush its audio playback buffer so that stale audio does not keep playing.
// Must be called before Login.
func (g *RdpClient) OnAudioReset(f func()) *RdpClient {
	g.onAudioResetFn = f
	return g
}

// OnClipboard registers callbacks for bidirectional clipboard sharing.
//
//   - onRemote is called with the text when the RDP server's clipboard
//     content is received (server → client).
//   - getLocal is called to retrieve the current local clipboard text
//     when the server requests it (client → server).
//
// Must be called before Login.
func (g *RdpClient) OnClipboard(onRemote func(text string), getLocal func() string) *RdpClient {
	g.onClipboardFn = onRemote
	g.getClipboardFn = getLocal
	return g
}

// NotifyClipboardChanged tells the server that the local clipboard has
// changed.  The UI should call this when it detects a system clipboard
// change (e.g. via polling or a platform clipboard-change signal).
func (g *RdpClient) NotifyClipboardChanged() {
	if g.cliprdrHandler != nil {
		g.cliprdrHandler.OnLocalClipboardChanged()
	}
}

func (g *RdpClient) KeyUp(sc int) {
	if !g.eventReady {
		return
	}
	slog.Debug("KeyUp", "sc", sc)
	g.flushMouseMove()

	p := &pdu.ScancodeKeyEvent{}
	p.KeyCode = uint16(sc)
	p.KeyboardFlags |= pdu.KBDFLAGS_RELEASE
	g.pdu.SendInputEvents(pdu.INPUT_EVENT_SCANCODE, []pdu.InputEventsInterface{p})
}

func (g *RdpClient) KeyDown(sc int) {
	if !g.eventReady {
		return
	}
	slog.Debug("KeyDown", "sc", sc)
	g.flushMouseMove()

	p := &pdu.ScancodeKeyEvent{}
	p.KeyCode = uint16(sc)
	g.pdu.SendInputEvents(pdu.INPUT_EVENT_SCANCODE, []pdu.InputEventsInterface{p})
}

// MouseMove queues a mouse-move event.  Successive moves within
// mouseCoalesceInterval are collapsed: only the latest (x,y) is sent.  The
// first move in a burst is sent immediately so the server sees no extra
// latency for a single isolated motion.
func (g *RdpClient) MouseMove(x, y int) {
	if !g.eventReady {
		return
	}

	g.mouseMu.Lock()
	g.mouseX = x
	g.mouseY = y
	g.mousePending = true

	now := time.Now()
	since := now.Sub(g.mouseLastTx)
	if since >= mouseCoalesceInterval {
		// Throttle window has elapsed — send right away.
		g.sendMouseMoveLocked(now)
		g.mouseMu.Unlock()
		return
	}

	// Within throttle window: schedule a flush for the remainder of it
	// (unless one is already scheduled).
	if g.mouseTimer == nil {
		delay := mouseCoalesceInterval - since
		g.mouseTimer = time.AfterFunc(delay, g.flushMouseMoveTimer)
	}
	g.mouseMu.Unlock()
}

// flushMouseMove sends any pending mouse-move event synchronously.  Called
// before any non-move input event to preserve server-side ordering.
func (g *RdpClient) flushMouseMove() {
	g.mouseMu.Lock()
	if g.mouseTimer != nil {
		g.mouseTimer.Stop()
		g.mouseTimer = nil
	}
	if g.mousePending {
		g.sendMouseMoveLocked(time.Now())
	}
	g.mouseMu.Unlock()
}

// flushMouseMoveTimer is the time.AfterFunc callback.  Acquires the lock
// itself and sends whatever's pending.
func (g *RdpClient) flushMouseMoveTimer() {
	g.mouseMu.Lock()
	g.mouseTimer = nil
	if g.mousePending && g.eventReady {
		g.sendMouseMoveLocked(time.Now())
	}
	g.mouseMu.Unlock()
}

// sendMouseMoveLocked must be called with mouseMu held.
func (g *RdpClient) sendMouseMoveLocked(now time.Time) {
	p := &pdu.PointerEvent{
		PointerFlags: pdu.PTRFLAGS_MOVE,
		XPos:         uint16(g.mouseX),
		YPos:         uint16(g.mouseY),
	}
	g.mousePending = false
	g.mouseLastTx = now
	g.pdu.SendInputEvents(pdu.INPUT_EVENT_MOUSE, []pdu.InputEventsInterface{p})
}

func (g *RdpClient) MouseWheel(scroll int) {
	if !g.eventReady {
		return
	}
	slog.Debug("MouseWheel")
	g.flushMouseMove()

	p := &pdu.PointerEvent{}
	p.PointerFlags |= pdu.PTRFLAGS_WHEEL
	if scroll < 0 {
		p.PointerFlags |= pdu.PTRFLAGS_WHEEL_NEGATIVE
	}
	var ts uint8 = uint8(scroll)
	p.PointerFlags |= pdu.WheelRotationMask & uint16(ts)
	g.pdu.SendInputEvents(pdu.INPUT_EVENT_MOUSE, []pdu.InputEventsInterface{p})
}

func (g *RdpClient) MouseUp(button int, x, y int) {
	if !g.eventReady {
		return
	}
	slog.Debug("MouseUp", "x", x, "y", y, "button", button)
	g.flushMouseMove()
	p := &pdu.PointerEvent{}

	switch button {
	case 0:
		p.PointerFlags |= pdu.PTRFLAGS_BUTTON1
	case 2:
		p.PointerFlags |= pdu.PTRFLAGS_BUTTON2
	case 1:
		p.PointerFlags |= pdu.PTRFLAGS_BUTTON3
	default:
		p.PointerFlags |= pdu.PTRFLAGS_MOVE
	}

	p.XPos = uint16(x)
	p.YPos = uint16(y)
	g.pdu.SendInputEvents(pdu.INPUT_EVENT_MOUSE, []pdu.InputEventsInterface{p})
}

func (g *RdpClient) MouseDown(button int, x, y int) {
	if !g.eventReady {
		return
	}
	slog.Debug("MouseDown", "x", x, "y", y, "button", button)
	g.flushMouseMove()
	p := &pdu.PointerEvent{}

	p.PointerFlags |= pdu.PTRFLAGS_DOWN

	switch button {
	case 0:
		p.PointerFlags |= pdu.PTRFLAGS_BUTTON1
	case 2:
		p.PointerFlags |= pdu.PTRFLAGS_BUTTON2
	case 1:
		p.PointerFlags |= pdu.PTRFLAGS_BUTTON3
	default:
		p.PointerFlags |= pdu.PTRFLAGS_MOVE
	}

	p.XPos = uint16(x)
	p.YPos = uint16(y)
	g.pdu.SendInputEvents(pdu.INPUT_EVENT_MOUSE, []pdu.InputEventsInterface{p})
}

func (g *RdpClient) Reconnect(width, height int) error {
	if g.closed {
		return fmt.Errorf("client is closed")
	}

	slog.Debug("Reconnect", "width", width, "height", height)
	g.closeTransport()
	g.width = width
	g.height = height
	g.eventReady = false

	const maxRetries = 3
	for attempt := 1; attempt <= maxRetries; attempt++ {
		// Exponential backoff: 1s, 2s, 4s — gives the server time to
		// tear down the previous session before we reconnect.
		delay := time.Duration(1<<uint(attempt-1)) * time.Second
		slog.Debug("Reconnect: waiting before attempt", "attempt", attempt, "delay", delay)
		time.Sleep(delay)

		err := g.Login(g.domain, g.user, g.password)
		if err != nil {
			slog.Warn("Reconnect: login failed", "attempt", attempt, "err", err)
			if attempt < maxRetries {
				g.closeTransport()
				continue
			}
			return fmt.Errorf("[reconnect err] %v", err)
		}

		g.reregisterCallbacks()
		slog.Debug("Reconnect: succeeded", "attempt", attempt)
		return nil
	}

	return fmt.Errorf("[reconnect failed after %d attempts]", maxRetries)
}

func (g *RdpClient) reregisterCallbacks() {
	if g.onErrorFn != nil {
		g.OnError(g.onErrorFn)
	}
	if g.onCloseFn != nil {
		g.OnClose(g.onCloseFn)
	}
	if g.onSuccesFn != nil {
		g.OnSucces(g.onSuccesFn)
	}
	if g.onReadyFn != nil {
		g.OnReady(g.onReadyFn)
	}
	if g.onBitmapPaintFn != nil {
		g.OnBitmap(g.onBitmapPaintFn)
	}
	if g.onPointerHideFn != nil {
		g.OnPointerHide(g.onPointerHideFn)
	}
	if g.onPointerCachedFn != nil {
		g.OnPointerCached(g.onPointerCachedFn)
	}
	if g.onPointerUpdateFn != nil {
		g.OnPointerUpdate(g.onPointerUpdateFn)
	}
	if g.onAudioResetFn != nil {
		g.OnAudioReset(g.onAudioResetFn)
	}
}

// closeTransport closes the underlying transport.
func (g *RdpClient) closeTransport() {
	if g.tpkt != nil {
		g.tpkt.Close()
	}
}

func (g *RdpClient) Close() {
	slog.Debug("Close()")
	g.closed = true
	g.closeTransport()
}
