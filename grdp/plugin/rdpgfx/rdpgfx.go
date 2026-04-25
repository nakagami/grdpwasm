package rdpgfx

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nakagami/grdp/core"
	"github.com/nakagami/grdp/plugin"
)

// regionPool reuses byte slices for progressive codec rectangle extraction,
// avoiding per-rectangle allocations that cause GC pressure.
var regionPool = sync.Pool{
	New: func() any { return []byte(nil) },
}

const (
	ChannelName = plugin.RDPGFX_DVC_CHANNEL_NAME
)

// RDPGFX Command IDs (MS-RDPEGFX 2.2.2)
const (
	cmdidWireToSurface1           uint16 = 0x0001
	cmdidWireToSurface2           uint16 = 0x0002
	cmdidDeleteEncodingContext    uint16 = 0x0003
	cmdidSolidFill                uint16 = 0x0004
	cmdidSurfaceToSurface         uint16 = 0x0005
	cmdidSurfaceToCache           uint16 = 0x0006
	cmdidCacheToSurface           uint16 = 0x0007
	cmdidEvictCacheEntry          uint16 = 0x0008
	cmdidCreateSurface            uint16 = 0x0009
	cmdidDeleteSurface            uint16 = 0x000A
	cmdidStartFrame               uint16 = 0x000B
	cmdidEndFrame                 uint16 = 0x000C
	cmdidFrameAcknowledge         uint16 = 0x000D
	cmdidResetGraphics            uint16 = 0x000E
	cmdidMapSurfaceToOutput       uint16 = 0x000F
	cmdidCacheImportOffer         uint16 = 0x0010
	cmdidCacheImportReply         uint16 = 0x0011
	cmdidCapsAdvertise            uint16 = 0x0012
	cmdidCapsConfirm              uint16 = 0x0013
	cmdidMapSurfaceToScaledOutput  uint16 = 0x0015
	cmdidMapSurfaceToScaledWindow  uint16 = 0x0016
	cmdidMapSurfaceToScaledOutputV2 uint16 = 0x0017 // v10.6+
	cmdidMapSurfaceToWindow        uint16 = 0x0018
)

// Pixel Formats
const (
	pixelFormatXRGB8888 uint8 = 0x20
	pixelFormatARGB8888 uint8 = 0x21
)

// Codec IDs (MS-RDPEGFX 2.2.2.1 / FreeRDP rdpgfx.h)
const (
	codecUncompressed uint16 = 0x0000
	codecCaVideo      uint16 = 0x0003 // RDPGFX_CODECID_CAVIDEO (RemoteFX tiles)
	codecPlanar       uint16 = 0x0004
	codecProgressive  uint16 = 0x0009
	codecAVC420       uint16 = 0x000B
	codecAVC444       uint16 = 0x000E
	codecAVC444v2     uint16 = 0x000F
)

// Capability versions and flags
const (
	capVersion8          uint32 = 0x00080004
	capVersion81         uint32 = 0x00080105
	capVersion10         uint32 = 0x000A0002
	capVersion101        uint32 = 0x000A0100
	capVersion102        uint32 = 0x000A0200
	capVersion103        uint32 = 0x000A0301
	capVersion104        uint32 = 0x000A0400
	capVersion105        uint32 = 0x000A0502
	capVersion106        uint32 = 0x000A0600
	capVersion1061       uint32 = 0x000A0601
	capVersion107        uint32 = 0x000A0701
	capFlagThinClient    uint32 = 0x00000001
	capFlagSmallCache    uint32 = 0x00000002
	capFlagAVC420Enabled uint32 = 0x00000010 // v8.1: explicitly enable AVC420
	capFlagAVCDisabled   uint32 = 0x00000020 // v10+: disable AVC
)

const headerSize = 8

// BitmapUpdate represents a rendered bitmap region.
//
// Lifecycle: Data is borrowed from an internal buffer pool and is only
// valid for the duration of the synchronous onBitmap callback.  After the
// callback returns, the slice may be returned to the pool and overwritten
// by subsequent updates.  Callers that need to retain the pixels (e.g. to
// hand them to an asynchronous paint goroutine) MUST copy the bytes
// before the callback returns.
type BitmapUpdate struct {
	DestLeft, DestTop, DestRight, DestBottom int
	Width, Height                            int
	Bpp                                      int    // bytes per pixel (always 4)
	Data                                     []byte // BGRA pixel data — see lifecycle note above
}

// bitmapBufPool reuses BGRA byte slices used to back BitmapUpdate.Data.
// Buffers are acquired with acquireBitmapBuf, handed to the onBitmap
// callback, and released with releaseBitmapBuf once the (synchronous)
// callback returns.  This eliminates per-rectangle allocations on the
// hot CaVideo / AVC partial-blit paths.
var bitmapBufPool = sync.Pool{
	New: func() any { return []byte(nil) },
}

// decodePkt is the message type for the async decode channel.
// pooled is true when data was acquired from bitmapBufPool; the receiver
// must call releaseBitmapBuf(data) after processing.
type decodePkt struct {
	data   []byte
	pooled bool
}

func acquireBitmapBuf(size int) []byte {
	if size <= 0 {
		return nil
	}
	b := bitmapBufPool.Get().([]byte)
	if cap(b) < size {
		return make([]byte, size)
	}
	return b[:size]
}

func releaseBitmapBuf(b []byte) {
	if b == nil {
		return
	}
	//nolint:staticcheck // intentional pool of byte slices
	bitmapBufPool.Put(b[:cap(b)])
}

// emitAndReleaseUpdates calls the onBitmap callback and then returns the
// pooled Data buffers of the supplied updates back to bitmapBufPool.  All
// updates passed in must have Data acquired via acquireBitmapBuf.
func (g *GfxHandler) emitAndReleaseUpdates(updates []BitmapUpdate) {
	if g.onBitmap != nil && len(updates) > 0 {
		g.onBitmap(updates)
	}
	for i := range updates {
		releaseBitmapBuf(updates[i].Data)
		updates[i].Data = nil
	}
}

type surface struct {
	width, height uint16
	format        uint8
	data          []byte // BGRA, 4 bytes per pixel
	outputX       uint32
	outputY       uint32
	mapped        bool
}

type vBarEntry struct {
	pixels []byte // BGR data, 3 bytes per pixel
	count  int
}

type clearCodecCtx struct {
	vBarStorage      []vBarEntry
	shortVBarStorage []vBarEntry
	vBarCursor       int
	shortVBarCursor  int
}

func newClearCodecCtx() *clearCodecCtx {
	return &clearCodecCtx{
		vBarStorage:      make([]vBarEntry, 32768),
		shortVBarStorage: make([]vBarEntry, 16384),
	}
}

type cacheEntry struct {
	data          []byte // BGRA pixel data
	width, height int
}

// GfxHandler implements the RDPGFX (MS-RDPEGFX) protocol.
type GfxHandler struct {
	surfaces     map[uint16]*surface
	cacheEntries map[uint16]cacheEntry
	clearCtx     *clearCodecCtx
	zgfx         *zgfxContext
	rfx          *rfxDecoder
	progressive  *rfxProgressiveDecoder
	h264dec      h264Decoder
	// framesDecoded is accessed from both read and decode goroutines.
	framesDecoded atomic.Uint32
	sendFn        func(data []byte)
	onBitmap      func([]BitmapUpdate)
	// decodeCh receives decompressed PDU data for asynchronous decode.
	decodeCh chan decodePkt
	// ackCh is a buffered channel of serialized ACK PDUs.  Every
	// EndFrame ACK is enqueued here and the writeLoop goroutine sends
	// each one to the server.  The server tracks outstanding frames
	// individually, so skipping ACKs causes it to stop sending.
	ackCh chan []byte
	// onKeyframeNeeded is called when the H.264 decoder needs a fresh IDR
	// from the server.  The `force` flag is set true when the regular
	// SendRefreshRect nudge has been ignored several times — the caller
	// should then issue a stronger refresh (e.g. SuppressOutput off→on)
	// that Windows servers reliably honour during active video streaming.
	onKeyframeNeeded    func(force bool)
	keyframeRequested   bool
	keyframeAttempts    int
	lastKeyframeRequest time.Time
	// onDecoderBroken is called once when the H.264 decoder becomes permanently
	// unrecoverable.  The caller should reconnect the RDP session to create a
	// fresh decoder.
	onDecoderBroken        func()
	decoderBrokenNotified  bool
	// lastHardResetCount tracks the decoder's hard-reset count so we can
	// detect a new reset and immediately clear the keyframe rate-limit.
	// After a hard reset the decoder wants a fresh IDR urgently; we must
	// not wait up to 3 s for the rate-limit window to expire.
	lastHardResetCount int
	// onH264Raw is called with raw H.264 NAL unit data when h264dec is nil
	// (e.g. WASM builds without CGo).  The caller can forward the data to a
	// JavaScript WebCodecs VideoDecoder instead.
	// destX, destY are the top-left canvas coordinates.
	onH264Raw func(destX, destY, w, h int, isKey bool, data []byte)
}

// NewGfxHandler creates a new RDPGFX handler.
func NewGfxHandler(onBitmap func([]BitmapUpdate)) *GfxHandler {
	g := &GfxHandler{
		surfaces:     make(map[uint16]*surface),
		cacheEntries: make(map[uint16]cacheEntry),
		clearCtx:     newClearCodecCtx(),
		zgfx:         newZgfxContext(),
		rfx:          newRfxDecoder(),
		progressive:  newRfxProgressiveDecoder(),
		h264dec:      newH264Decoder(),
		onBitmap:     onBitmap,
		decodeCh:     make(chan decodePkt, 1024),
		ackCh:        make(chan []byte, 512),
	}
	go g.decodeLoop()
	go g.writeLoop()
	return g
}

// SetSendFunc sets the function used to send RDPGFX responses via DVC.
func (g *GfxHandler) SetSendFunc(fn func([]byte)) {
	g.sendFn = fn
}

// SetKeyframeNeededCallback registers a function that is called once when the
// H.264 decoder resets to software mode and needs a fresh keyframe (IDR) from
// the server.  The callback should send a screen-refresh request so the server
// starts a new GOP and decoding can resume promptly.
func (g *GfxHandler) SetKeyframeNeededCallback(fn func(force bool)) {
	g.onKeyframeNeeded = fn
}

// SetDecoderBrokenCallback registers a function that is called once when the
// H.264 decoder becomes permanently unrecoverable (all hard-reset retries
// exhausted).  The callback should reconnect the RDP session so a fresh
// decoder can be created from scratch.
func (g *GfxHandler) SetDecoderBrokenCallback(fn func()) {
	g.onDecoderBroken = fn
}

// SetH264RawCallback registers a function that receives raw H.264 NAL unit
// data when the built-in decoder is unavailable (h264dec == nil).  This
// allows the caller to hand off decoding to an external engine such as the
// browser WebCodecs VideoDecoder API.
//
// destX and destY are the top-left canvas coordinates of the decoded frame.
// isKey is true when the NAL data starts a new GOP (IDR frame).
func (g *GfxHandler) SetH264RawCallback(fn func(destX, destY, w, h int, isKey bool, data []byte)) {
	g.onH264Raw = fn
}

// OnChannelCreated is called after the DVC CREATE_RSP has been sent.
// It sends CAPS_ADVERTISE to the server to initiate the RDPGFX pipeline.
func (g *GfxHandler) OnChannelCreated() {
	g.sendCapsAdvertise()
}

// sendCapsAdvertise sends RDPGFX_CAPS_ADVERTISE_PDU to the server.
// The client must advertise its capabilities before the server will
// send any graphics data (MS-RDPEGFX 2.2.3.1).
func (g *GfxHandler) sendCapsAdvertise() {
	p := &bytes.Buffer{}

	if g.h264dec != nil {
		// Advertise capsets in ascending order (v8.0 → v10.7), matching
		// rdpyqt / FreeRDP layout so servers pick the highest common version.
		core.WriteUInt16LE(11, p) // capsSetCount

		// v8.0 — baseline fallback (no AVC)
		core.WriteUInt32LE(capVersion8, p)
		core.WriteUInt32LE(4, p)
		core.WriteUInt32LE(capFlagThinClient, p)

		// v8.1 — AVC420 via explicit flag
		core.WriteUInt32LE(capVersion81, p)
		core.WriteUInt32LE(4, p)
		core.WriteUInt32LE(capFlagSmallCache|capFlagAVC420Enabled, p)

		// v10.0
		core.WriteUInt32LE(capVersion10, p)
		core.WriteUInt32LE(4, p)
		core.WriteUInt32LE(capFlagSmallCache, p)

		// v10.1 — 16-byte capsData (12 zero bytes after flags)
		core.WriteUInt32LE(capVersion101, p)
		core.WriteUInt32LE(16, p)
		core.WriteUInt32LE(0, p)
		core.WriteUInt32LE(0, p)
		core.WriteUInt32LE(0, p)
		core.WriteUInt32LE(0, p)

		// v10.2
		core.WriteUInt32LE(capVersion102, p)
		core.WriteUInt32LE(4, p)
		core.WriteUInt32LE(capFlagSmallCache, p)

		// v10.3
		core.WriteUInt32LE(capVersion103, p)
		core.WriteUInt32LE(4, p)
		core.WriteUInt32LE(0, p)

		// v10.4
		core.WriteUInt32LE(capVersion104, p)
		core.WriteUInt32LE(4, p)
		core.WriteUInt32LE(capFlagSmallCache, p)

		// v10.5
		core.WriteUInt32LE(capVersion105, p)
		core.WriteUInt32LE(4, p)
		core.WriteUInt32LE(capFlagSmallCache, p)

		// v10.6
		core.WriteUInt32LE(capVersion106, p)
		core.WriteUInt32LE(4, p)
		core.WriteUInt32LE(capFlagSmallCache, p)

		// v10.6.1
		core.WriteUInt32LE(capVersion1061, p)
		core.WriteUInt32LE(4, p)
		core.WriteUInt32LE(capFlagSmallCache, p)

		// v10.7
		core.WriteUInt32LE(capVersion107, p)
		core.WriteUInt32LE(4, p)
		core.WriteUInt32LE(capFlagSmallCache, p)

		g.sendPdu(cmdidCapsAdvertise, p.Bytes())
		slog.Debug("RDPGFX: sent CAPS_ADVERTISE (v10.7..v8.0, AVC enabled)")
	} else {
		core.WriteUInt16LE(1, p) // capsSetCount
		core.WriteUInt32LE(capVersion8, p)
		core.WriteUInt32LE(4, p) // capsDataLength
		// Use flags that intentionally cause servers to reject the RDPGFX
		// channel, forcing fallback to surface bitmap commands (NSCodec /
		// RemoteFX). We do not yet support ClearCodec (0x0008) or Planar
		// (0x0009) which servers send over RDPGFX when it stays open.
		core.WriteUInt32LE(capFlagThinClient|capFlagSmallCache|capFlagAVCDisabled, p)
		g.sendPdu(cmdidCapsAdvertise, p.Bytes())
		slog.Debug("RDPGFX: sent CAPS_ADVERTISE (v8.0)")
	}
}

// ZGFX segment descriptors (MS-RDPEGFX 2.2.4)
const (
	zgfxSingle    = 0xE0
	zgfxMultipart = 0xE1

	zgfxCompressedRDP8 = 0x04
)

// Process handles a complete RDPGFX payload (may contain multiple PDUs).
// Data arrives wrapped in ZGFX (RDP8 Bulk Compression) segments (MS-RDPEGFX 2.2.4).
//
// Called on the network read goroutine.  Decompression happens here;
// the decompressed payload is then queued for asynchronous processing
// (including frame ACKs and decode) on the decode goroutine.
// This keeps the read goroutine free from any socket.Write calls that
// could cause TCP deadlock when both sides try to write simultaneously.
func (g *GfxHandler) Process(data []byte) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("RDPGFX: panic in Process", "err", r)
		}
	}()
	if len(data) < 1 {
		return
	}

	var decompressed []byte
	var decompPooled bool

	descriptor := data[0]
	switch descriptor {
	case zgfxSingle:
		if len(data) < 2 {
			return
		}
		decompressed, decompPooled = g.decompressSegment(data[1:])
	case zgfxMultipart:
		decompressed, decompPooled = g.decompressMultipart(data[1:])
	default:
		slog.Warn("RDPGFX: unknown ZGFX descriptor", "descriptor", fmt.Sprintf("0x%02X", descriptor))
		decompressed = data
	}

	if len(decompressed) == 0 {
		return
	}

	// decompressSegment / decompressMultipart already return owned
	// buffers (freshly allocated or copied from input) so we can hand
	// the slice directly to the async decode goroutine.
	pkt := decodePkt{data: decompressed, pooled: decompPooled}
	select {
	case g.decodeCh <- pkt:
	default:
		// Channel full — video decode is dropped, but we must still
		// ACK any EndFrame PDUs so the server's outstanding-frame
		// count stays accurate and it keeps sending.
		slog.Warn("RDPGFX: decodeCh full, dropping frame (ACKs preserved)", "queueCap", cap(g.decodeCh))
		g.ackDroppedFrames(pkt)
	}
}

// ackDroppedFrames scans decompressed PDU data for EndFrame commands
// and sends ACKs for them.  Called on the read goroutine when decodeCh
// is full and the message is being dropped.  Without this, dropped
// EndFrames would leave the server's outstanding-frame count stuck,
// eventually causing it to stop sending entirely.
func (g *GfxHandler) ackDroppedFrames(pkt decodePkt) {
	data := pkt.data
	defer func() {
		if pkt.pooled {
			releaseBitmapBuf(data)
		}
	}()
	for offset := 0; offset+headerSize <= len(data); {
		cmdId := binary.LittleEndian.Uint16(data[offset:])
		pduLength := binary.LittleEndian.Uint32(data[offset+4:])
		if pduLength < uint32(headerSize) || int(pduLength) > len(data)-offset {
			break
		}
		if cmdId == cmdidEndFrame {
			pduData := data[offset+headerSize : offset+int(pduLength)]
			if len(pduData) >= 4 {
				g.sendFrameAck(binary.LittleEndian.Uint32(pduData))
			}
		}
		offset += int(pduLength)
	}
}

// decompressSegment handles a single ZGFX segment (after the descriptor byte).
// First byte is RDP8_BULK_ENCODED_DATA header:
//
//	bits 0-3: compression type (0x04 = RDP8)
//	bit 5: PACKET_COMPRESSED (0x20)
//
// Always returns pooled=true: the returned slice was acquired from
// bitmapBufPool (directly or via Decompress) and must be released with
// releaseBitmapBuf once the caller is done with it.
func (g *GfxHandler) decompressSegment(seg []byte) ([]byte, bool) {
	if len(seg) < 1 {
		return nil, false
	}
	header := seg[0]
	payload := seg[1:]
	if header&0x20 != 0 {
		// Acquire a pool buffer as the initial backing for Decompress output.
		// Decompress may grow beyond it; the returned slice (not buf) must be
		// released by the caller.  Any over-small buf that gets replaced is
		// abandoned to GC — the pool converges to the right size over time.
		buf := acquireBitmapBuf(len(payload) * 3)
		return g.zgfx.Decompress(payload, buf), true
	}
	g.zgfx.historyWrite(payload)
	// Return a pooled copy: payload aliases the caller's network buffer, which
	// will be reused on the next read. Callers hand the slice off to the async
	// decode goroutine and must own the memory.
	buf := acquireBitmapBuf(len(payload))
	copy(buf, payload)
	return buf, true
}

// decompressMultipart handles ZGFX multipart segments and returns the
// concatenated decompressed data (without processing PDUs).
// Returns a slice acquired from bitmapBufPool; caller must release it.
func (g *GfxHandler) decompressMultipart(data []byte) ([]byte, bool) {
	if len(data) < 6 {
		return nil, false
	}
	// Direct slice indexing — avoids bytes.NewReader and per-field io.ReadFull.
	segCount := binary.LittleEndian.Uint16(data[0:])
	uncompSize := binary.LittleEndian.Uint32(data[2:])
	offset := 6

	// Pre-allocate to the advertised uncompressed size to avoid repeated
	// buffer growths as each segment is appended.
	buf := acquireBitmapBuf(int(uncompSize))
	result := buf[:0]
	for i := uint16(0); i < segCount; i++ {
		if offset+4 > len(data) {
			break
		}
		segSize := int(binary.LittleEndian.Uint32(data[offset:]))
		offset += 4
		if offset+segSize > len(data) {
			break
		}
		segData := data[offset : offset+segSize]
		offset += segSize
		raw, rawPooled := g.decompressSegment(segData)
		if raw != nil {
			result = append(result, raw...)
			if rawPooled {
				releaseBitmapBuf(raw)
			}
		}
	}
	if len(result) == 0 {
		releaseBitmapBuf(buf)
		return nil, false
	}
	// If result grew beyond buf, buf was abandoned; result is the new owner.
	return result, true
}

// decodeLoop runs in a dedicated goroutine, reading decompressed PDU data
// from decodeCh and dispatching all processing — including frame ACKs and
// heavy decode work.  Keeping socket.Write calls off the read goroutine
// avoids TCP deadlock (where both sides try to write while neither reads).
// It automatically restarts on panic.
func (g *GfxHandler) decodeLoop() {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("RDPGFX: panic in decodeLoop, restarting", "err", r)
			go g.decodeLoop()
		}
	}()
	slog.Debug("RDPGFX: decodeLoop started")
	for pkt := range g.decodeCh {
		g.decodePDUs(pkt.data)
		if pkt.pooled {
			releaseBitmapBuf(pkt.data)
		}
	}
}

// skipHeavyThreshold controls when CaVideo/progressive decode is skipped.
// When the queue has more items than this, heavy decode is skipped to drain
// the backlog quickly.  A small threshold means we decode almost every frame
// during normal playback, only skipping under severe backpressure.
const skipHeavyThreshold = 16

// decodePDUs processes all PDUs in decompressed data.
// Frame ACKs (EndFrame) are ALWAYS processed so the server gets timely
// acknowledgements.  Heavy CaVideo/progressive decode is skipped when
// the queue is significantly backed up.
func (g *GfxHandler) decodePDUs(data []byte) {
	skipHeavy := len(g.decodeCh) > skipHeavyThreshold

	for offset := 0; offset+headerSize <= len(data); {
		cmdId := binary.LittleEndian.Uint16(data[offset:])
		pduLength := binary.LittleEndian.Uint32(data[offset+4:])
		if pduLength < uint32(headerSize) || int(pduLength) > len(data)-offset {
			break
		}
		pduData := data[offset+headerSize : offset+int(pduLength)]
		g.dispatchDecode(cmdId, pduData, skipHeavy)
		offset += int(pduLength)
	}
}

// dispatchDecode routes a single PDU.  When skipHeavy is true, CaVideo
// and progressive decode are skipped to drain the queue quickly.
// EndFrame (frame ACK) is always processed regardless of skipHeavy.
func (g *GfxHandler) dispatchDecode(cmdId uint16, data []byte, skipHeavy bool) {
	switch cmdId {
	case cmdidCapsConfirm:
		g.onCapsConfirm(data)
	case cmdidResetGraphics:
		g.onResetGraphics(data)
	case cmdidCreateSurface:
		g.onCreateSurface(data)
	case cmdidDeleteSurface:
		g.onDeleteSurface(data)
	case cmdidMapSurfaceToOutput:
		g.onMapSurfaceToOutput(data)
	case cmdidStartFrame:
		// nothing to do
	case cmdidEndFrame:
		g.onEndFrame(data) // always ACK, even when skipHeavy
	case cmdidWireToSurface1:
		g.onWireToSurface1Decode(data, skipHeavy)
	case cmdidWireToSurface2:
		g.onWireToSurface2Decode(data, skipHeavy)
	case cmdidSolidFill:
		g.onSolidFill(data)
	case cmdidCacheToSurface:
		g.onCacheToSurface(data)
	case cmdidEvictCacheEntry:
		g.onEvictCacheEntry(data)
	case cmdidCacheImportOffer:
		g.onCacheImportOffer()
	case cmdidMapSurfaceToWindow, cmdidMapSurfaceToScaledWindow:
		// ignored — we don't support per-window mapping
	case cmdidMapSurfaceToScaledOutput, cmdidMapSurfaceToScaledOutputV2:
		g.onMapSurfaceToScaledOutput(data)
	default:
		slog.Debug("RDPGFX: unhandled cmd", "cmdId", fmt.Sprintf("0x%04X", cmdId))
	}
}

// writeLoop runs in a dedicated goroutine.  It reads serialized ACK
// PDUs from ackCh and sends each one via sendFn.  Every ACK must reach
// the server — the server tracks outstanding frames individually and
// stops sending if ACKs are missing.  Automatically restarts on panic.
func (g *GfxHandler) writeLoop() {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("RDPGFX: panic in writeLoop, restarting", "err", r)
			go g.writeLoop()
		}
	}()
	for pdu := range g.ackCh {
		if g.sendFn != nil {
			g.sendFn(pdu)
		}
	}
}

// sendPdu sends a PDU synchronously.  Used for rare control messages
// (CapsAdvertise, CacheImportReply) that must not be dropped.
func (g *GfxHandler) sendPdu(cmdId uint16, payload []byte) {
	if g.sendFn == nil {
		return
	}
	b := &bytes.Buffer{}
	core.WriteUInt16LE(cmdId, b)
	core.WriteUInt16LE(0, b) // flags
	core.WriteUInt32LE(uint32(headerSize+len(payload)), b)
	b.Write(payload)
	g.sendFn(b.Bytes())
}

// sendPduAsync enqueues a PDU for the writeLoop goroutine to send.
// Every ACK must be delivered to the server, so this uses a buffered
// channel rather than a single-value "latest" slot.
func (g *GfxHandler) sendPduAsync(cmdId uint16, payload []byte) {
	b := &bytes.Buffer{}
	core.WriteUInt16LE(cmdId, b)
	core.WriteUInt16LE(0, b) // flags
	core.WriteUInt32LE(uint32(headerSize+len(payload)), b)
	b.Write(payload)
	select {
	case g.ackCh <- b.Bytes():
	default:
		slog.Warn("RDPGFX: ackCh full, ACK dropped")
	}
}

// --- Command Handlers ---

func (g *GfxHandler) onCapsConfirm(data []byte) {
	if len(data) < 12 {
		slog.Debug("RDPGFX: CAPS_CONFIRM received (short)")
		return
	}
	r := bytes.NewReader(data)
	version, _ := core.ReadUInt32LE(r)
	dataLen, _ := core.ReadUInt32LE(r)
	flags := uint32(0)
	if dataLen >= 4 {
		flags, _ = core.ReadUInt32LE(r)
	}
	slog.Debug("RDPGFX: CAPS_CONFIRM", "version", fmt.Sprintf("0x%08X", version), "flags", fmt.Sprintf("0x%08X", flags))
}

func (g *GfxHandler) onResetGraphics(data []byte) {
	if len(data) < 12 {
		return
	}
	r := bytes.NewReader(data)
	w, _ := core.ReadUInt32LE(r)
	h, _ := core.ReadUInt32LE(r)
	slog.Debug("RDPGFX: RESET_GRAPHICS", "w", w, "h", h)
	g.surfaces = make(map[uint16]*surface)
	g.clearCtx = newClearCodecCtx()
	g.framesDecoded.Store(0)
	if g.h264dec != nil {
		g.h264dec.Close()
		g.h264dec = newH264Decoder()
	}
}

func (g *GfxHandler) onCreateSurface(data []byte) {
	if len(data) < 7 {
		return
	}
	r := bytes.NewReader(data)
	id, _ := core.ReadUint16LE(r)
	w, _ := core.ReadUint16LE(r)
	h, _ := core.ReadUint16LE(r)
	f, _ := core.ReadUInt8(r)
	slog.Debug("RDPGFX: CREATE_SURFACE", "id", id, "w", w, "h", h)
	g.surfaces[id] = &surface{
		width: w, height: h, format: f,
		data: make([]byte, int(w)*int(h)*4),
	}
}

func (g *GfxHandler) onDeleteSurface(data []byte) {
	if len(data) < 2 {
		return
	}
	id := binary.LittleEndian.Uint16(data)
	delete(g.surfaces, id)
}

func (g *GfxHandler) onMapSurfaceToOutput(data []byte) {
	if len(data) < 12 {
		return
	}
	r := bytes.NewReader(data)
	id, _ := core.ReadUint16LE(r)
	core.ReadUint16LE(r) // reserved
	ox, _ := core.ReadUInt32LE(r)
	oy, _ := core.ReadUInt32LE(r)
	slog.Debug("RDPGFX: MAP_SURFACE", "id", id, "ox", ox, "oy", oy)
	if s, ok := g.surfaces[id]; ok {
		s.outputX = ox
		s.outputY = oy
		s.mapped = true
	}
}

func (g *GfxHandler) onMapSurfaceToScaledOutput(data []byte) {
	if len(data) < 20 {
		return
	}
	r := bytes.NewReader(data)
	id, _ := core.ReadUint16LE(r)
	core.ReadUint16LE(r) // reserved
	ox, _ := core.ReadUInt32LE(r)
	oy, _ := core.ReadUInt32LE(r)
	core.ReadUInt32LE(r) // targetWidth
	core.ReadUInt32LE(r) // targetHeight
	slog.Debug("RDPGFX: MAP_SURFACE_SCALED", "id", id, "ox", ox, "oy", oy)
	if s, ok := g.surfaces[id]; ok {
		s.outputX = ox
		s.outputY = oy
		s.mapped = true
	}
}

// sendFrameAck builds and queues a FRAME_ACKNOWLEDGE PDU.
// Safe to call from any goroutine (uses atomic framesDecoded).
// The PDU is serialized directly into a 20-byte slice to avoid
// the two bytes.Buffer allocations the previous implementation required.
func (g *GfxHandler) sendFrameAck(frameId uint32) {
	decoded := g.framesDecoded.Add(1)
	// 8-byte RDPGFX header + 12-byte FRAME_ACKNOWLEDGE payload = 20 bytes.
	pdu := make([]byte, 20)
	binary.LittleEndian.PutUint16(pdu[0:], cmdidFrameAcknowledge)
	// pdu[2:4] = flags (0) — zero value
	binary.LittleEndian.PutUint32(pdu[4:], 20) // total PDU length
	// pdu[8:12] = queue (0) — zero value
	binary.LittleEndian.PutUint32(pdu[12:], frameId)
	binary.LittleEndian.PutUint32(pdu[16:], decoded)
	select {
	case g.ackCh <- pdu:
	default:
		slog.Warn("RDPGFX: ackCh full, ACK dropped")
	}
}

func (g *GfxHandler) onEndFrame(data []byte) {
	if len(data) < 4 {
		return
	}
	g.sendFrameAck(binary.LittleEndian.Uint32(data))
}

// onWireToSurface1Decode handles RDPGFX_WIRE_TO_SURFACE_PDU_1 (MS-RDPEGFX 2.2.2.1).
func (g *GfxHandler) onWireToSurface1Decode(data []byte, skipHeavy bool) {
	if len(data) < 17 {
		return
	}
	// Parse fixed header fields via direct binary indexing (avoids bytes.NewReader
	// and per-field io.ReadFull overhead on the hot H.264 path).
	surfId := binary.LittleEndian.Uint16(data[0:])
	codecId := binary.LittleEndian.Uint16(data[2:])
	pixFmt := data[4]
	left := binary.LittleEndian.Uint16(data[5:])
	top := binary.LittleEndian.Uint16(data[7:])
	right := binary.LittleEndian.Uint16(data[9:])
	bottom := binary.LittleEndian.Uint16(data[11:])
	bmpLen := binary.LittleEndian.Uint32(data[13:])
	if int(bmpLen) > len(data)-17 {
		return
	}
	bmpData := data[17 : 17+int(bmpLen)]

	if slog.Default().Enabled(nil, slog.LevelDebug) {
		slog.Debug("RDPGFX: WTS1", "surfId", surfId, "codecId", fmt.Sprintf("0x%04X", codecId),
			"w", right-left, "h", bottom-top, "bmpLen", bmpLen)
	}

	w := int(right - left)
	h := int(bottom - top)
	if w <= 0 || h <= 0 {
		return
	}

	s, ok := g.surfaces[surfId]
	if !ok {
		return
	}

	// CaVideo (0x0003) carries RFX tile-encoded data; decode onto the
	// persistent surface buffer like the progressive codec in WTS2.
	if codecId == codecCaVideo {
		if skipHeavy {
			return
		}
		rects := g.rfx.Decode(bmpData, int(left), int(top), s.data, int(s.width), int(s.height))
		if !s.mapped || g.onBitmap == nil || len(rects) == 0 {
			return
		}
		updates := make([]BitmapUpdate, 0, len(rects))
		stride := int(s.width) * 4
		for _, rc := range rects {
			needed := rc.w * rc.h * 4
			region := acquireBitmapBuf(needed)
			rowBytes := rc.w * 4
			for row := 0; row < rc.h; row++ {
				srcOff := (rc.y+row)*stride + rc.x*4
				dstOff := row * rowBytes
				if srcOff+rowBytes <= len(s.data) {
					copy(region[dstOff:dstOff+rowBytes], s.data[srcOff:srcOff+rowBytes])
				}
			}
			destL := int(s.outputX) + rc.x
			destT := int(s.outputY) + rc.y
			updates = append(updates, BitmapUpdate{
				DestLeft: destL, DestTop: destT,
				DestRight: destL + rc.w - 1, DestBottom: destT + rc.h - 1,
				Width: rc.w, Height: rc.h, Bpp: 4, Data: region,
			})
		}
		g.emitAndReleaseUpdates(updates)
		return
	}

	var decoded []byte
	var avcRegions []avcRect
	owned := false // true ⇒ decoded buffer is from bitmapBufPool and must be released
	switch codecId {
	case codecUncompressed:
		decoded = decodeUncompressed(bmpData, w, h, pixFmt)
		owned = true
	case codecPlanar:
		decoded = decodePlanar(bmpData, w, h)
		owned = true
	case codecAVC420:
		var ownedAVC bool
		decoded, avcRegions, ownedAVC = g.decodeAVC420(bmpData, int(s.outputX)+int(left), int(s.outputY)+int(top), w, h)
		owned = ownedAVC
	case codecAVC444, codecAVC444v2:
		var ownedAVC bool
		decoded, avcRegions, ownedAVC = g.decodeAVC444(bmpData, int(s.outputX)+int(left), int(s.outputY)+int(top), w, h)
		owned = ownedAVC
	default:
		slog.Warn("RDPGFX: unsupported codec in WTS1", "codecId", fmt.Sprintf("0x%04X", codecId), "surfId", surfId, "w", w, "h", h, "bmpLen", bmpLen)
		return
	}
	if decoded == nil {
		return
	}

	if len(avcRegions) > 0 && shouldUseAVCRegions(avcRegions, w, h) {
		g.blitAndEmitAVCRegions(s, int(left), int(top), w, h, decoded, avcRegions)
		if owned {
			releaseBitmapBuf(decoded)
		}
		return
	}

	blitToSurface(s, int(left), int(top), w, h, decoded)
	if owned {
		g.emitBitmapPooled(s, int(left), int(top), w, h, decoded)
	} else {
		g.emitBitmap(s, int(left), int(top), w, h, decoded)
	}
}

// onWireToSurface2Decode handles RDPGFX_WIRE_TO_SURFACE_PDU_2 (MS-RDPEGFX 2.2.2.2).
func (g *GfxHandler) onWireToSurface2Decode(data []byte, skipHeavy bool) {
	if len(data) < 13 {
		return
	}
	// Parse fixed header fields via direct binary indexing (avoids bytes.NewReader
	// and per-field io.ReadFull overhead on the hot H.264 path).
	surfId := binary.LittleEndian.Uint16(data[0:])
	codecId := binary.LittleEndian.Uint16(data[2:])
	codecCtxId := binary.LittleEndian.Uint32(data[4:])
	pixFmt := data[8]
	bmpLen := binary.LittleEndian.Uint32(data[9:])
	if int(bmpLen) > len(data)-13 {
		return
	}
	bmpData := data[13 : 13+int(bmpLen)]

	s, ok := g.surfaces[surfId]
	if !ok {
		return
	}

	w := int(s.width)
	h := int(s.height)

	if slog.Default().Enabled(nil, slog.LevelDebug) {
		slog.Debug("RDPGFX: WTS2", "surfId", surfId, "codecId", fmt.Sprintf("0x%04X", codecId),
			"w", w, "h", h, "bmpLen", bmpLen)
	}

	var decoded []byte
	switch codecId {
	case codecUncompressed:
		decoded = decodeUncompressed(bmpData, w, h, pixFmt)
		blitToSurface(s, 0, 0, w, h, decoded)
		g.emitBitmapPooled(s, 0, 0, w, h, decoded)
	case codecPlanar:
		decoded = decodePlanar(bmpData, w, h)
		blitToSurface(s, 0, 0, w, h, decoded)
		g.emitBitmapPooled(s, 0, 0, w, h, decoded)
	case codecCaVideo:
		if skipHeavy {
			break // frame drop
		}
		rects := g.rfx.Decode(bmpData, 0, 0, s.data, w, h)
		if s.mapped && g.onBitmap != nil && len(rects) > 0 {
			updates := make([]BitmapUpdate, 0, len(rects))
			stride := w * 4
			for _, rc := range rects {
				needed := rc.w * rc.h * 4
				region := acquireBitmapBuf(needed)
				rowBytes := rc.w * 4
				for row := 0; row < rc.h; row++ {
					srcOff := (rc.y+row)*stride + rc.x*4
					dstOff := row * rowBytes
					if srcOff+rowBytes <= len(s.data) {
						copy(region[dstOff:dstOff+rowBytes], s.data[srcOff:srcOff+rowBytes])
					}
				}
				destL := int(s.outputX) + rc.x
				destT := int(s.outputY) + rc.y
				updates = append(updates, BitmapUpdate{
					DestLeft: destL, DestTop: destT,
					DestRight: destL + rc.w - 1, DestBottom: destT + rc.h - 1,
					Width: rc.w, Height: rc.h, Bpp: 4, Data: region,
				})
			}
			g.emitAndReleaseUpdates(updates)
		}
	case codecAVC420:
		decoded, avcRegions, ownedAVC := g.decodeAVC420(bmpData, int(s.outputX), int(s.outputY), w, h)
		if decoded != nil {
			if len(avcRegions) > 0 && shouldUseAVCRegions(avcRegions, w, h) {
				g.blitAndEmitAVCRegions(s, 0, 0, w, h, decoded, avcRegions)
				if ownedAVC {
					releaseBitmapBuf(decoded)
				}
			} else {
				blitToSurface(s, 0, 0, w, h, decoded)
				if ownedAVC {
					g.emitBitmapPooled(s, 0, 0, w, h, decoded)
				} else {
					g.emitBitmap(s, 0, 0, w, h, decoded)
				}
			}
		}
	case codecAVC444, codecAVC444v2:
		decoded, avcRegions, ownedAVC := g.decodeAVC444(bmpData, int(s.outputX), int(s.outputY), w, h)
		if decoded != nil {
			if len(avcRegions) > 0 && shouldUseAVCRegions(avcRegions, w, h) {
				g.blitAndEmitAVCRegions(s, 0, 0, w, h, decoded, avcRegions)
				if ownedAVC {
					releaseBitmapBuf(decoded)
				}
			} else {
				blitToSurface(s, 0, 0, w, h, decoded)
				if ownedAVC {
					g.emitBitmapPooled(s, 0, 0, w, h, decoded)
				} else {
					g.emitBitmap(s, 0, 0, w, h, decoded)
				}
			}
		}
	case codecProgressive:
		if skipHeavy {
			break // frame drop
		}
		// Decode tiles directly onto the persistent surface buffer.
		rects := g.progressive.Decode(bmpData, s.data, w, h)
		for _, rc := range rects {
			needed := rc.w * rc.h * 4
			region := regionPool.Get().([]byte)
			if cap(region) < needed {
				region = make([]byte, needed)
			} else {
				region = region[:needed]
			}
			stride := w * 4
			rowBytes := rc.w * 4
			for row := 0; row < rc.h; row++ {
				srcOff := (rc.y+row)*stride + rc.x*4
				dstOff := row * rowBytes
				if srcOff+rowBytes <= len(s.data) {
					copy(region[dstOff:dstOff+rowBytes], s.data[srcOff:srcOff+rowBytes])
				}
			}
			g.emitBitmap(s, rc.x, rc.y, rc.w, rc.h, region)
			regionPool.Put(region)
		}
	default:
		slog.Debug("RDPGFX: WTS2 unsupported codec", "codecId", fmt.Sprintf("0x%04X", codecId), "ctxId", codecCtxId)
		return
	}
}

func (g *GfxHandler) onSolidFill(data []byte) {
	if len(data) < 8 {
		return
	}
	r := bytes.NewReader(data)
	surfId, _ := core.ReadUint16LE(r)
	cb, _ := core.ReadUInt8(r)
	cg, _ := core.ReadUInt8(r)
	cr, _ := core.ReadUInt8(r)
	core.ReadUInt8(r) // XA
	fillCount, _ := core.ReadUint16LE(r)

	s, ok := g.surfaces[surfId]
	if !ok {
		return
	}

	stride := int(s.width) * 4
	// Pre-compose a single BGRA pixel as a uint32 for one-shot writes.
	pixelU32 := uint32(cb) | uint32(cg)<<8 | uint32(cr)<<16 | uint32(0xFF)<<24

	for i := uint16(0); i < fillCount; i++ {
		left, _ := core.ReadUint16LE(r)
		top, _ := core.ReadUint16LE(r)
		right, _ := core.ReadUint16LE(r)
		bottom, _ := core.ReadUint16LE(r)
		w := int(right - left)
		h := int(bottom - top)
		if w <= 0 || h <= 0 {
			continue
		}

		// Clamp to surface bounds
		yEnd := int(bottom)
		if yEnd > int(s.height) {
			yEnd = int(s.height)
		}
		xEnd := int(right)
		if xEnd > int(s.width) {
			xEnd = int(s.width)
		}

		// Fill the first row with PutUint32 (single 32-bit store per pixel),
		// then replicate it to subsequent rows with copy().
		rowStart := int(top)*stride + int(left)*4
		rowBytes := (xEnd - int(left)) * 4
		if rowStart+rowBytes <= len(s.data) {
			row := s.data[rowStart : rowStart+rowBytes]
			for x := 0; x+4 <= rowBytes; x += 4 {
				binary.LittleEndian.PutUint32(row[x:], pixelU32)
			}
			for y := int(top) + 1; y < yEnd; y++ {
				dst := y*stride + int(left)*4
				if dst+rowBytes <= len(s.data) {
					copy(s.data[dst:dst+rowBytes], row)
				}
			}
		}

		if s.mapped && g.onBitmap != nil {
			// Build fill data: fill first row, then replicate (doubling).
			fillData := acquireBitmapBuf(w * h * 4)
			rowW := w * 4
			for x := 0; x+4 <= rowW; x += 4 {
				binary.LittleEndian.PutUint32(fillData[x:], pixelU32)
			}
			// Doubling copy: O(log h) memmoves instead of h linear copies.
			filled := rowW
			total := rowW * h
			for filled*2 <= total {
				copy(fillData[filled:filled*2], fillData[:filled])
				filled *= 2
			}
			if filled < total {
				copy(fillData[filled:total], fillData[:total-filled])
			}
			destL := int(s.outputX) + int(left)
			destT := int(s.outputY) + int(top)
			g.emitAndReleaseUpdates([]BitmapUpdate{{
				DestLeft: destL, DestTop: destT,
				DestRight: destL + w - 1, DestBottom: destT + h - 1,
				Width: w, Height: h, Bpp: 4, Data: fillData,
			}})
		}
	}
}

func (g *GfxHandler) onCacheToSurface(data []byte) {
	if len(data) < 6 {
		return
	}
	r := bytes.NewReader(data)
	cacheSlot, _ := core.ReadUint16LE(r)
	surfId, _ := core.ReadUint16LE(r)
	destCount, _ := core.ReadUint16LE(r)

	ce, hasCE := g.cacheEntries[cacheSlot]
	s, hasSurf := g.surfaces[surfId]

	for i := uint16(0); i < destCount; i++ {
		dx, _ := core.ReadUint16LE(r)
		dy, _ := core.ReadUint16LE(r)
		if hasCE && hasSurf {
			blitToSurface(s, int(dx), int(dy), ce.width, ce.height, ce.data)
			g.emitBitmap(s, int(dx), int(dy), ce.width, ce.height, ce.data)
		}
	}
}

func (g *GfxHandler) onEvictCacheEntry(data []byte) {
	if len(data) < 2 {
		return
	}
	slot := binary.LittleEndian.Uint16(data)
	delete(g.cacheEntries, slot)
}

func (g *GfxHandler) onCacheImportOffer() {
	p := &bytes.Buffer{}
	core.WriteUInt16LE(0, p) // importedEntriesCount = 0
	g.sendPdu(cmdidCacheImportReply, p.Bytes())
}

// --- Helpers ---

func blitToSurface(s *surface, x, y, w, h int, src []byte) {
	stride := int(s.width) * 4
	for row := 0; row < h; row++ {
		dy := y + row
		if dy < 0 || dy >= int(s.height) {
			continue
		}
		srcOff := row * w * 4
		dstOff := dy*stride + x*4
		n := w * 4
		if dstOff >= 0 && dstOff+n <= len(s.data) && srcOff+n <= len(src) {
			copy(s.data[dstOff:dstOff+n], src[srcOff:srcOff+n])
		}
	}
}

// emitBitmapPooled is like emitBitmap but releases `decoded` back to
// bitmapBufPool after the synchronous onBitmap callback returns.  Use this
// for codec output buffers that the GfxHandler owns end-to-end (currently
// uncompressed and planar).
func (g *GfxHandler) emitBitmapPooled(s *surface, x, y, w, h int, decoded []byte) {
	if !s.mapped || g.onBitmap == nil {
		releaseBitmapBuf(decoded)
		return
	}
	destL := int(s.outputX) + x
	destT := int(s.outputY) + y
	g.emitAndReleaseUpdates([]BitmapUpdate{{
		DestLeft: destL, DestTop: destT,
		DestRight: destL + w - 1, DestBottom: destT + h - 1,
		Width: w, Height: h, Bpp: 4, Data: decoded,
	}})
}

func (g *GfxHandler) emitBitmap(s *surface, x, y, w, h int, decoded []byte) {
	if !s.mapped || g.onBitmap == nil {
		return
	}
	destL := int(s.outputX) + x
	destT := int(s.outputY) + y
	g.onBitmap([]BitmapUpdate{{
		DestLeft: destL, DestTop: destT,
		DestRight: destL + w - 1, DestBottom: destT + h - 1,
		Width: w, Height: h, Bpp: 4, Data: decoded,
	}})
}

// --- Codec: Uncompressed ---

func decodeUncompressed(data []byte, w, h int, pixFmt uint8) []byte {
	out := acquireBitmapBuf(w * h * 4)
	n := w * h * 4
	if len(data) >= n {
		copy(out, data[:n])
	} else {
		copy(out[:len(data)], data)
		// Zero the unfilled tail in case the slice was reused from the pool.
		for i := len(data); i < n; i++ {
			out[i] = 0
		}
	}
	return out
}

// --- Codec: Planar (RDP 6.0 Bitmap Codec, MS-RDPEGDI 2.2.2.5) ---

func decodePlanar(data []byte, w, h int) []byte {
	if len(data) < 1 {
		return acquireBitmapBuf(w * h * 4)
	}
	header := data[0]
	rle := (header >> 5) & 1
	noAlpha := (header >> 6) & 1
	planeSize := w * h
	offset := 1

	var alphaPlane, redPlane, greenPlane, bluePlane []byte
	if rle == 0 {
		if noAlpha == 0 {
			alphaPlane, offset = readRawPlane(data, offset, planeSize)
		}
		redPlane, offset = readRawPlane(data, offset, planeSize)
		greenPlane, offset = readRawPlane(data, offset, planeSize)
		bluePlane, offset = readRawPlane(data, offset, planeSize)
	} else {
		if noAlpha == 0 {
			alphaPlane, offset = decodeNRLE(data, offset, planeSize)
		}
		redPlane, offset = decodeNRLE(data, offset, planeSize)
		greenPlane, offset = decodeNRLE(data, offset, planeSize)
		bluePlane, offset = decodeNRLE(data, offset, planeSize)
	}
	_ = offset

	applyDelta(alphaPlane, w, h)
	applyDelta(redPlane, w, h)
	applyDelta(greenPlane, w, h)
	applyDelta(bluePlane, w, h)

	out := acquireBitmapBuf(planeSize * 4)
	// Hoist the per-pixel nil/length checks: clamp each plane to
	// `planeSize` (zero-fill missing planes) so the inner loop has no
	// branches and the bounds checks are eliminated.
	rp := planeOrZero(redPlane, planeSize)
	gp := planeOrZero(greenPlane, planeSize)
	bp := planeOrZero(bluePlane, planeSize)
	ap := alphaPlane
	hasAlpha := ap != nil && len(ap) >= planeSize
	if hasAlpha {
		ap = ap[:planeSize]
		for i := 0; i < planeSize; i++ {
			j := i * 4
			out[j] = bp[i]
			out[j+1] = gp[i]
			out[j+2] = rp[i]
			out[j+3] = ap[i]
		}
	} else {
		for i := 0; i < planeSize; i++ {
			j := i * 4
			out[j] = bp[i]
			out[j+1] = gp[i]
			out[j+2] = rp[i]
			out[j+3] = 0xFF
		}
	}
	return out
}

// planeOrZero returns a slice of exactly `size` bytes, either the input
// plane (truncated if longer) or a zero-filled buffer when the plane is
// nil or short.  Used to drop per-pixel nil/bounds checks in decodePlanar.
func planeOrZero(plane []byte, size int) []byte {
	if len(plane) >= size {
		return plane[:size]
	}
	out := make([]byte, size)
	copy(out, plane)
	return out
}

func readRawPlane(data []byte, offset, size int) ([]byte, int) {
	plane := make([]byte, size)
	end := offset + size
	if end > len(data) {
		end = len(data)
	}
	if offset < end {
		copy(plane, data[offset:end])
	}
	return plane, offset + size
}

func decodeNRLE(data []byte, offset, planeSize int) ([]byte, int) {
	out := make([]byte, planeSize)
	pos := 0
	for pos < planeSize && offset < len(data) {
		ctrl := data[offset]
		offset++
		runLen := int((ctrl >> 4) & 0x0F)
		rawLen := int(ctrl & 0x0F)

		if runLen == 15 {
			if offset >= len(data) {
				break
			}
			ext := int(data[offset])
			offset++
			runLen += ext
			if ext == 0xFF {
				if offset+1 >= len(data) {
					break
				}
				runLen += int(binary.LittleEndian.Uint16(data[offset:]))
				offset += 2
			}
		}
		for i := 0; i < runLen && pos < planeSize; i++ {
			out[pos] = 0
			pos++
		}

		if rawLen == 15 {
			if offset >= len(data) {
				break
			}
			ext := int(data[offset])
			offset++
			rawLen += ext
			if ext == 0xFF {
				if offset+1 >= len(data) {
					break
				}
				rawLen += int(binary.LittleEndian.Uint16(data[offset:]))
				offset += 2
			}
		}
		for i := 0; i < rawLen && pos < planeSize && offset < len(data); i++ {
			out[pos] = data[offset]
			pos++
			offset++
		}
	}
	return out, offset
}

func applyDelta(plane []byte, w, h int) {
	if plane == nil || len(plane) < w*h {
		return
	}
	// Process row-by-row: previous row XORs into current row using
	// fixed-length slices so the compiler eliminates per-element
	// bounds checks and the index multiplications hoist.
	for y := 1; y < h; y++ {
		base := y * w
		prev := plane[base-w : base : base]
		cur := plane[base : base+w : base+w]
		for x := 0; x < w; x++ {
			cur[x] ^= prev[x]
		}
	}
}

// --- Codec: ClearCodec (MS-RDPEGFX 2.2.4) ---

func (ctx *clearCodecCtx) decode(data []byte, w, h int) []byte {
	if len(data) < 12 {
		return make([]byte, w*h*4)
	}
	r := bytes.NewReader(data)
	residualLen, _ := core.ReadUInt32LE(r)
	bandsLen, _ := core.ReadUInt32LE(r)
	subcodecLen, _ := core.ReadUInt32LE(r)

	out := make([]byte, w*h*4)
	if residualLen > 0 {
		residual, _ := core.ReadBytes(int(residualLen), r)
		decodeResidual(residual, w, h, out)
	}
	if bandsLen > 0 {
		bands, _ := core.ReadBytes(int(bandsLen), r)
		ctx.decodeBands(bands, w, out)
	}
	if subcodecLen > 0 {
		subcodec, _ := core.ReadBytes(int(subcodecLen), r)
		decodeSubcodec(subcodec, w, out)
	}
	return out
}

func decodeResidual(data []byte, w, h int, out []byte) {
	for y := 0; y < h; y++ {
		rowDstStart := y * w * 4
		rowSrcStart := y * w * 3
		rowDstEnd := rowDstStart + w*4
		rowSrcEnd := rowSrcStart + w*3
		if rowDstEnd > len(out) || rowSrcEnd > len(data) {
			return
		}
		dst := out[rowDstStart:rowDstEnd:rowDstEnd]
		src := data[rowSrcStart:rowSrcEnd:rowSrcEnd]
		for x := 0; x < w; x++ {
			si := x * 3
			di := x * 4
			dst[di] = src[si]
			dst[di+1] = src[si+1]
			dst[di+2] = src[si+2]
			dst[di+3] = 0xFF
		}
	}
}

func (ctx *clearCodecCtx) decodeBands(data []byte, surfW int, out []byte) {
	r := bytes.NewReader(data)
	for r.Len() >= 11 {
		xStart, _ := core.ReadUint16LE(r)
		yStart, _ := core.ReadUint16LE(r)
		xEnd, _ := core.ReadUint16LE(r)
		yEnd, _ := core.ReadUint16LE(r)
		blueBg, _ := core.ReadUInt8(r)
		greenBg, _ := core.ReadUInt8(r)
		redBg, _ := core.ReadUInt8(r)

		bandHeight := int(yEnd - yStart)
		colCount := int(xEnd - xStart)
		if bandHeight <= 0 || colCount <= 0 {
			continue
		}

		for col := 0; col < colCount; col++ {
			if r.Len() < 2 {
				return
			}
			vBarHeader, _ := core.ReadUint16LE(r)
			x := int(xStart) + col

			if (vBarHeader & 0xC000) == 0xC000 {
				// SHORT_VBAR_CACHE_HIT
				idx := int(vBarHeader & 0x3FFF)
				if idx < len(ctx.shortVBarStorage) {
					entry := ctx.shortVBarStorage[idx]
					paintColumnBg(out, surfW, x, int(yStart), bandHeight, redBg, greenBg, blueBg)
					paintVBarPixels(out, surfW, x, int(yStart), 0, entry)
				}
			} else if (vBarHeader & 0xC000) == 0x4000 {
				// SHORT_VBAR_CACHE_MISS
				pixCount := int(vBarHeader & 0x3FFF)
				if r.Len() < 1 {
					return
				}
				yOn, _ := core.ReadUInt8(r)
				pixels := make([]byte, pixCount*3)
				if r.Len() >= pixCount*3 {
					rp, _ := core.ReadBytes(pixCount*3, r)
					copy(pixels, rp)
				} else {
					rp, _ := core.ReadBytes(r.Len(), r)
					copy(pixels, rp)
				}
				entry := vBarEntry{pixels: pixels, count: pixCount}
				if ctx.shortVBarCursor < len(ctx.shortVBarStorage) {
					ctx.shortVBarStorage[ctx.shortVBarCursor] = entry
				}
				ctx.shortVBarCursor = (ctx.shortVBarCursor + 1) % len(ctx.shortVBarStorage)
				paintColumnBg(out, surfW, x, int(yStart), bandHeight, redBg, greenBg, blueBg)
				paintVBarPixels(out, surfW, x, int(yStart), int(yOn), entry)
			} else if (vBarHeader & 0x8000) == 0x8000 {
				// VBAR_CACHE_HIT
				idx := int(vBarHeader & 0x7FFF)
				if idx < len(ctx.vBarStorage) {
					entry := ctx.vBarStorage[idx]
					paintVBarPixels(out, surfW, x, int(yStart), 0, entry)
				}
			} else {
				// VBAR_CACHE_MISS
				pixCount := int(vBarHeader & 0x7FFF)
				pixels := make([]byte, pixCount*3)
				if r.Len() >= pixCount*3 {
					rp, _ := core.ReadBytes(pixCount*3, r)
					copy(pixels, rp)
				} else {
					rp, _ := core.ReadBytes(r.Len(), r)
					copy(pixels, rp)
				}
				entry := vBarEntry{pixels: pixels, count: pixCount}
				if ctx.vBarCursor < len(ctx.vBarStorage) {
					ctx.vBarStorage[ctx.vBarCursor] = entry
				}
				ctx.vBarCursor = (ctx.vBarCursor + 1) % len(ctx.vBarStorage)
				paintVBarPixels(out, surfW, x, int(yStart), 0, entry)
			}
		}
	}
}

func paintColumnBg(out []byte, surfW, x, yStart, height int, r, g, b uint8) {
	if x < 0 || surfW <= 0 || x >= surfW {
		return
	}
	for y := 0; y < height; y++ {
		dy := yStart + y
		idx := (dy*surfW + x) * 4
		if idx < 0 || idx+4 > len(out) {
			continue
		}
		px := out[idx : idx+4 : idx+4]
		px[0] = b
		px[1] = g
		px[2] = r
		px[3] = 0xFF
	}
}

func paintVBarPixels(out []byte, surfW, x, yStart, yOn int, entry vBarEntry) {
	if x < 0 || surfW <= 0 || x >= surfW {
		return
	}
	pixels := entry.pixels
	for y := 0; y < entry.count; y++ {
		si := y * 3
		dy := yStart + yOn + y
		di := (dy*surfW + x) * 4
		if si+3 > len(pixels) || di < 0 || di+4 > len(out) {
			continue
		}
		src := pixels[si : si+3 : si+3]
		px := out[di : di+4 : di+4]
		px[0] = src[0]
		px[1] = src[1]
		px[2] = src[2]
		px[3] = 0xFF
	}
}

func decodeSubcodec(data []byte, surfW int, out []byte) {
	r := bytes.NewReader(data)
	for r.Len() >= 13 {
		xStart, _ := core.ReadUint16LE(r)
		yStart, _ := core.ReadUint16LE(r)
		width, _ := core.ReadUint16LE(r)
		height, _ := core.ReadUint16LE(r)
		bmpLen, _ := core.ReadUInt32LE(r)
		subcodecId, _ := core.ReadUInt8(r)
		if int(bmpLen) > r.Len() {
			break
		}
		bmpData, _ := core.ReadBytes(int(bmpLen), r)

		if subcodecId == 0 {
			// RAW BGR
			rowSrc := int(width) * 3
			rowDst := surfW * 4
			for y := 0; y < int(height); y++ {
				srcStart := y * rowSrc
				srcEnd := srcStart + rowSrc
				dy := int(yStart) + y
				dstStart := dy*rowDst + int(xStart)*4
				dstEnd := dstStart + int(width)*4
				if srcEnd > len(bmpData) || dstStart < 0 || dstEnd > len(out) {
					continue
				}
				src := bmpData[srcStart:srcEnd:srcEnd]
				dst := out[dstStart:dstEnd:dstEnd]
				for x := 0; x < int(width); x++ {
					si := x * 3
					di := x * 4
					dst[di] = src[si]
					dst[di+1] = src[si+1]
					dst[di+2] = src[si+2]
					dst[di+3] = 0xFF
				}
			}
		}
		// Skip NSCodec (1) and glyph (2) subcodecs
	}
}
