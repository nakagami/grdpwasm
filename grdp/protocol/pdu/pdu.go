package pdu

import (
	"bytes"
	"log/slog"
	"sync"

	"github.com/nakagami/grdp/core"
	"github.com/nakagami/grdp/emission"
	"github.com/nakagami/grdp/protocol/t125/gcc"
)

var readerPool = sync.Pool{
	New: func() any { return new(bytes.Reader) },
}

type PDULayer struct {
	emission.Emitter
	transport          core.Transport
	sharedId           uint32
	userId             uint16
	channelId          uint16
	serverCapabilities map[CapsType]Capability
	clientCapabilities map[CapsType]Capability
	fastPathSender     core.FastPathSender
	// serverFastPathInput is set after capability exchange when both sides
	// advertise INPUT_FLAG_FASTPATH_INPUT, allowing client input to be sent
	// using the much shorter fast-path framing (MS-RDPBCGR §2.2.8.1.2).
	serverFastPathInput bool
	demandActivePDU     *DemandActivePDU
}

func NewPDULayer(t core.Transport) *PDULayer {
	p := &PDULayer{
		Emitter:   *emission.NewEmitter(),
		transport: t,
		sharedId:  0x103EA,
		serverCapabilities: map[CapsType]Capability{
			CAPSTYPE_GENERAL: &GeneralCapability{
				ProtocolVersion: 0x0200,
			},
			CAPSTYPE_BITMAP: &BitmapCapability{
				Receive1BitPerPixel:      0x0001,
				Receive4BitsPerPixel:     0x0001,
				Receive8BitsPerPixel:     0x0001,
				BitmapCompressionFlag:    0x0001,
				MultipleRectangleSupport: 0x0001,
			},
			CAPSTYPE_ORDER: &OrderCapability{
				DesktopSaveXGranularity: 1,
				DesktopSaveYGranularity: 20,
				MaximumOrderLevel:       1,
				OrderFlags:              NEGOTIATEORDERSUPPORT,
				DesktopSaveSize:         480 * 480,
			},
			CAPSTYPE_POINTER:        &PointerCapability{ColorPointerCacheSize: 20},
			CAPSTYPE_INPUT:          &InputCapability{},
			CAPSTYPE_VIRTUALCHANNEL: &VirtualChannelCapability{},
			CAPSTYPE_FONT:           &FontCapability{SupportFlags: 0x0001},
			CAPSTYPE_COLORCACHE:     &ColorCacheCapability{CacheSize: 0x0006},
			CAPSTYPE_SHARE:          &ShareCapability{},
		},
		clientCapabilities: map[CapsType]Capability{
			CAPSTYPE_GENERAL: &GeneralCapability{
				ProtocolVersion: 0x0200,
			},
			CAPSTYPE_BITMAP: &BitmapCapability{
				Receive1BitPerPixel:      0x0001,
				Receive4BitsPerPixel:     0x0001,
				Receive8BitsPerPixel:     0x0001,
				BitmapCompressionFlag:    0x0001,
				MultipleRectangleSupport: 0x0001,
			},
			CAPSTYPE_ORDER: &OrderCapability{
				DesktopSaveXGranularity: 1,
				DesktopSaveYGranularity: 20,
				MaximumOrderLevel:       1,
				OrderFlags:              NEGOTIATEORDERSUPPORT,
				DesktopSaveSize:         480 * 480,
				TextANSICodePage:        0x4e4,
			},
			CAPSTYPE_CONTROL:         &ControlCapability{0, 0, 2, 2},
			CAPSTYPE_ACTIVATION:      &WindowActivationCapability{},
			CAPSTYPE_POINTER:         &PointerCapability{1, 20, 20},
			CAPSTYPE_SHARE:           &ShareCapability{},
			CAPSTYPE_COLORCACHE:      &ColorCacheCapability{6, 0},
			CAPSTYPE_SOUND:           &SoundCapability{0x0001, 0},
			CAPSTYPE_INPUT:           &InputCapability{},
			CAPSTYPE_FONT:            &FontCapability{0x0001, 0},
			CAPSTYPE_BRUSH:           &BrushCapability{BRUSH_COLOR_8x8},
			CAPSTYPE_GLYPHCACHE:      &GlyphCapability{},
			CAPSETTYPE_BITMAP_CODECS: newClientBitmapCodecsCapability(),
			CAPSTYPE_BITMAPCACHE_REV2: &BitmapCache2Capability{
				BitmapCachePersist: 2,
				CachesNum:          5,
				BmpC0Cells:         0x258,
				BmpC1Cells:         0x258,
				BmpC2Cells:         0x800,
				BmpC3Cells:         0x1000,
				BmpC4Cells:         0x800,
			},
			CAPSTYPE_VIRTUALCHANNEL:        &VirtualChannelCapability{0, 1600},
			CAPSETTYPE_MULTIFRAGMENTUPDATE: &MultiFragmentUpdate{0x3F0000},
			CAPSTYPE_RAIL: &RemoteProgramsCapability{
				RailSupportLevel: RAIL_LEVEL_SUPPORTED |
					RAIL_LEVEL_SHELL_INTEGRATION_SUPPORTED |
					RAIL_LEVEL_LANGUAGE_IME_SYNC_SUPPORTED |
					RAIL_LEVEL_SERVER_TO_CLIENT_IME_SYNC_SUPPORTED |
					RAIL_LEVEL_HIDE_MINIMIZED_APPS_SUPPORTED |
					RAIL_LEVEL_WINDOW_CLOAKING_SUPPORTED |
					RAIL_LEVEL_HANDSHAKE_EX_SUPPORTED |
					RAIL_LEVEL_DOCKED_LANGBAR_SUPPORTED,
			},
			CAPSETTYPE_LARGE_POINTER: &LargePointerCapability{1},
			CAPSETTYPE_COMPDESK: &DesktopCompositionCapability{
				CompDeskSupportLevel: 1, // COMPDESK_SUPPORTED
			},
			CAPSETTYPE_SURFACE_COMMANDS: &SurfaceCommandsCapability{
				CmdFlags: SURFCMDS_SET_SURFACE_BITS | SURFCMDS_STREAM_SURFACE_BITS | SURFCMDS_FRAME_MARKER,
			},
			CAPSSETTYPE_FRAME_ACKNOWLEDGE: &FrameAcknowledgeCapability{2},
		},
	}

	t.On("close", func() {
		p.Emit("close")
	}).On("error", func(err error) {
		p.Emit("error", err)
	})
	return p
}

func (p *PDULayer) sendPDU(message PDUMessage) {
	pdu := NewPDU(p.userId, message)
	p.transport.Write(pdu.serialize())
}

func (p *PDULayer) sendDataPDU(message DataPDUData) {
	dataPdu := NewDataPDU(message, p.sharedId)
	p.sendPDU(dataPdu)
}

func (p *PDULayer) SetFastPathSender(f core.FastPathSender) {
	p.fastPathSender = f
}

type Client struct {
	*PDULayer
	clientCoreData *gcc.ClientCoreData
	buff           *bytes.Buffer
}

func NewClient(t core.Transport) *Client {
	c := &Client{
		PDULayer: NewPDULayer(t),
		buff:     &bytes.Buffer{},
	}
	c.transport.Once("connect", c.connect)
	return c
}

func (c *Client) connect(data *gcc.ClientCoreData, userId uint16, channelId uint16) {
	slog.Debug("pdu connect", "userId", userId, "channelId", channelId)
	c.clientCoreData = data
	c.userId = userId
	c.channelId = channelId
	c.transport.Once("data", c.recvDemandActivePDU)
}

func (c *Client) recvDemandActivePDU(s []byte) {
	r := readerPool.Get().(*bytes.Reader)
	r.Reset(s)
	defer readerPool.Put(r)
	pdu, err := readPDU(r)
	if err != nil {
		slog.Error("recvDemandActivePDU", "err", err)
		return
	}
	if pdu.ShareCtrlHeader.PDUType != PDUTYPE_DEMANDACTIVEPDU {
		if pdu.ShareCtrlHeader.PDUType == PDUTYPE_DEACTIVATEALLPDU {
			slog.Error("server sent DeactivateAllPDU before session was established; the server likely failed to create a session (check xrdp/sesman logs on the server)")
			c.Emit("deactivateAll")
			return
		}
		if pdu.ShareCtrlHeader.PDUType == PDUTYPE_SERVER_REDIR_PKT {
			if redir, ok := pdu.Message.(*ServerRedirectionPDU); ok {
				c.Emit("redirect", redir)
			}
			return
		}
		slog.Debug("ignore message during connection sequence", "type", pdu.ShareCtrlHeader.PDUType)
		c.transport.Once("data", c.recvDemandActivePDU)
		return
	}
	c.sharedId = pdu.Message.(*DemandActivePDU).SharedId
	c.demandActivePDU = pdu.Message.(*DemandActivePDU)
	for _, caps := range c.demandActivePDU.CapabilitySets {
		slog.Debug("serverCaps", "type", caps.Type(), "value", caps)
		c.serverCapabilities[caps.Type()] = caps
	}
	if ic, ok := c.serverCapabilities[CAPSTYPE_INPUT].(*InputCapability); ok {
		c.serverFastPathInput = ic.Flags&INPUT_FLAG_FASTPATH_INPUT != 0
	}

	c.sendConfirmActivePDU()
	c.sendClientFinalizeSynchronizePDU()
	c.transport.Once("data", c.recvServerSynchronizePDU)
}

func (c *Client) sendConfirmActivePDU() {
	pdu := NewConfirmActivePDU()
	generalCapa := c.clientCapabilities[CAPSTYPE_GENERAL].(*GeneralCapability)
	generalCapa.OSMajorType = OSMAJORTYPE_WINDOWS
	generalCapa.OSMinorType = OSMINORTYPE_WINDOWS_NT
	generalCapa.ExtraFlags = LONG_CREDENTIALS_SUPPORTED | NO_BITMAP_COMPRESSION_HDR |
		FASTPATH_OUTPUT_SUPPORTED | AUTORECONNECT_SUPPORTED
	generalCapa.RefreshRectSupport = 1
	generalCapa.SuppressOutputSupport = 1

	bitmapCapa := c.clientCapabilities[CAPSTYPE_BITMAP].(*BitmapCapability)
	bitmapCapa.PreferredBitsPerPixel = 32
	bitmapCapa.DesktopWidth = c.clientCoreData.DesktopWidth
	bitmapCapa.DesktopHeight = c.clientCoreData.DesktopHeight
	bitmapCapa.DesktopResizeFlag = 0x0001

	orderCapa := c.clientCapabilities[CAPSTYPE_ORDER].(*OrderCapability)
	orderCapa.OrderFlags = NEGOTIATEORDERSUPPORT | ZEROBOUNDSDELTASSUPPORT | COLORINDEXSUPPORT | ORDERFLAGS_EXTRA_FLAGS
	orderCapa.OrderSupportExFlags |= ORDERFLAGS_EX_ALTSEC_FRAME_MARKER_SUPPORT
	orderCapa.OrderSupport[TS_NEG_DSTBLT_INDEX] = 1
	orderCapa.OrderSupport[TS_NEG_PATBLT_INDEX] = 1
	orderCapa.OrderSupport[TS_NEG_SCRBLT_INDEX] = 1
	//orderCapa.OrderSupport[TS_NEG_LINETO_INDEX] = 1
	//orderCapa.OrderSupport[TS_NEG_MEMBLT_INDEX] = 1
	//orderCapa.OrderSupport[TS_NEG_MEM3BLT_INDEX] = 1
	//orderCapa.OrderSupport[TS_NEG_POLYLINE_INDEX] = 1
	/*orderCapa.OrderSupport[TS_NEG_MULTIOPAQUERECT_INDEX] = 1
	orderCapa.OrderSupport[TS_NEG_GLYPH_INDEX_INDEX] = 1
	//orderCapa.OrderSupport[TS_NEG_DRAWNINEGRID_INDEX] = 1
	orderCapa.OrderSupport[TS_NEG_SAVEBITMAP_INDEX] = 1
	orderCapa.OrderSupport[TS_NEG_POLYGON_SC_INDEX] = 1
	orderCapa.OrderSupport[TS_NEG_POLYGON_CB_INDEX] = 1
	orderCapa.OrderSupport[TS_NEG_ELLIPSE_SC_INDEX] = 1
	orderCapa.OrderSupport[TS_NEG_ELLIPSE_CB_INDEX] = 1*/
	//orderCapa.OrderSupport[TS_NEG_FAST_GLYPH_INDEX] = 1

	inputCapa := c.clientCapabilities[CAPSTYPE_INPUT].(*InputCapability)
	inputCapa.Flags = INPUT_FLAG_SCANCODES | INPUT_FLAG_MOUSEX | INPUT_FLAG_UNICODE |
		INPUT_FLAG_FASTPATH_INPUT | INPUT_FLAG_FASTPATH_INPUT2
	inputCapa.KeyboardLayout = c.clientCoreData.KbdLayout
	inputCapa.KeyboardType = c.clientCoreData.KeyboardType
	inputCapa.KeyboardSubType = c.clientCoreData.KeyboardSubType
	inputCapa.KeyboardFunctionKey = c.clientCoreData.KeyboardFnKeys
	inputCapa.ImeFileName = c.clientCoreData.ImeFileName

	glyphCapa := c.clientCapabilities[CAPSTYPE_GLYPHCACHE].(*GlyphCapability)
	/*glyphCapa.GlyphCache[0] = cacheEntry{254, 4}
	glyphCapa.GlyphCache[1] = cacheEntry{254, 4}
	glyphCapa.GlyphCache[2] = cacheEntry{254, 8}
	glyphCapa.GlyphCache[3] = cacheEntry{254, 8}
	glyphCapa.GlyphCache[4] = cacheEntry{254, 16}
	glyphCapa.GlyphCache[5] = cacheEntry{254, 32}
	glyphCapa.GlyphCache[6] = cacheEntry{254, 64}
	glyphCapa.GlyphCache[7] = cacheEntry{254, 128}
	glyphCapa.GlyphCache[8] = cacheEntry{254, 256}
	glyphCapa.GlyphCache[9] = cacheEntry{64, 2048}
	glyphCapa.FragCache = 0x01000100*/
	glyphCapa.SupportLevel = GLYPH_SUPPORT_NONE

	pdu.SharedId = c.sharedId
	for _, v := range c.clientCapabilities {
		slog.Debug("clientCaps", "type", v.Type(), "value", v)
		pdu.CapabilitySets = append(pdu.CapabilitySets, v)
	}
	pdu.NumberCapabilities = uint16(len(pdu.CapabilitySets))
	pdu.LengthSourceDescriptor = c.demandActivePDU.LengthSourceDescriptor
	pdu.SourceDescriptor = c.demandActivePDU.SourceDescriptor
	pdu.LengthCombinedCapabilities = c.demandActivePDU.LengthCombinedCapabilities

	c.sendPDU(pdu)
}

func (c *Client) sendClientFinalizeSynchronizePDU() {
	c.sendDataPDU(NewSynchronizeDataPDU(c.channelId))
	c.sendDataPDU(&ControlDataPDU{Action: CTRLACTION_COOPERATE})
	c.sendDataPDU(&ControlDataPDU{Action: CTRLACTION_REQUEST_CONTROL})
	//c.sendDataPDU(&PersistKeyPDU{BBitMask: 0x03})
	c.sendDataPDU(&FontListDataPDU{ListFlags: 0x0003, EntrySize: 0x0032})
}

func (c *Client) recvServerSynchronizePDU(s []byte) {
	r := readerPool.Get().(*bytes.Reader)
	r.Reset(s)
	defer readerPool.Put(r)
	pdu, err := readPDU(r)
	if err != nil {
		slog.Error("recvServerSynchronizePDU", "err", err)
		return
	}
	dataPdu, ok := pdu.Message.(*DataPDU)
	if !ok || dataPdu.Header.PDUType2 != PDUTYPE2_SYNCHRONIZE {
		if ok {
			slog.Error("recvServerSynchronizePDU ignore datapdu", "type2", dataPdu.Header.PDUType2)
		} else {
			slog.Error("recvServerSynchronizePDU ignore message", "type", pdu.ShareCtrlHeader.PDUType)
		}
		slog.Debug("recvServerSynchronizePDU dataPdu", "pdu", &dataPdu)
		c.transport.Once("data", c.recvServerSynchronizePDU)
		return
	}
	c.transport.Once("data", c.recvServerControlCooperatePDU)
}

func (c *Client) recvServerControlCooperatePDU(s []byte) {
	r := readerPool.Get().(*bytes.Reader)
	r.Reset(s)
	defer readerPool.Put(r)
	pdu, err := readPDU(r)
	if err != nil {
		slog.Error("recvServerControlCooperatePDU", "err", err)
		return
	}
	dataPdu, ok := pdu.Message.(*DataPDU)
	if !ok || dataPdu.Header.PDUType2 != PDUTYPE2_CONTROL {
		if ok {
			slog.Error("recvServerControlCooperatePDU ignore datapdu", "type2", dataPdu.Header.PDUType2)
		} else {
			slog.Error("recvServerControlCooperatePDU ignore message", "type", pdu.ShareCtrlHeader.PDUType)
		}
		c.transport.Once("data", c.recvServerControlCooperatePDU)
		return
	}
	if dataPdu.Data.(*ControlDataPDU).Action != CTRLACTION_COOPERATE {
		slog.Error("recvServerControlCooperatePDU ignore", "action", dataPdu.Data.(*ControlDataPDU).Action)
		c.transport.Once("data", c.recvServerControlCooperatePDU)
		return
	}
	c.transport.Once("data", c.recvServerControlGrantedPDU)
}

func (c *Client) recvServerControlGrantedPDU(s []byte) {
	r := readerPool.Get().(*bytes.Reader)
	r.Reset(s)
	defer readerPool.Put(r)
	pdu, err := readPDU(r)
	if err != nil {
		slog.Error("recvServerControlGrantedPDU", "err", err)
		return
	}
	dataPdu, ok := pdu.Message.(*DataPDU)
	if !ok || dataPdu.Header.PDUType2 != PDUTYPE2_CONTROL {
		if ok {
			slog.Error("recvServerControlGrantedPDU ignore datapdu", "type2", dataPdu.Header.PDUType2)
		} else {
			slog.Error("recvServerControlGrantedPDU ignore message", "type", pdu.ShareCtrlHeader.PDUType)
		}
		c.transport.Once("data", c.recvServerControlGrantedPDU)
		return
	}
	if dataPdu.Data.(*ControlDataPDU).Action != CTRLACTION_GRANTED_CONTROL {
		slog.Error("recvServerControlGrantedPDU ignore", "action", dataPdu.Data.(*ControlDataPDU).Action)
		c.transport.Once("data", c.recvServerControlGrantedPDU)
		return
	}
	c.transport.Once("data", c.recvServerFontMapPDU)
}

func (c *Client) recvServerFontMapPDU(s []byte) {
	r := readerPool.Get().(*bytes.Reader)
	r.Reset(s)
	defer readerPool.Put(r)
	pdu, err := readPDU(r)
	if err != nil {
		slog.Error("recvServerFontMapPDU", "err", err)
		return
	}
	dataPdu, ok := pdu.Message.(*DataPDU)
	if !ok || dataPdu.Header.PDUType2 != PDUTYPE2_FONTMAP {
		if ok {
			slog.Error("recvServerFontMapPDU ignore datapdu", "type2", dataPdu.Header.PDUType2)
		} else {
			slog.Error("recvServerFontMapPDU ignore message", "type", pdu.ShareCtrlHeader.PDUType)
		}
		return
	}
	c.transport.On("data", c.recvPDU)

	// Tell the server we're ready to receive display updates (MS-RDPBCGR 2.2.11.3.1)
	slog.Debug("Sending SuppressOutput (ALLOW_DISPLAY_UPDATES)")
	c.sendDataPDU(&SuppressOutputPDU{
		AllowDisplayUpdates: 1,
		Right:               c.clientCoreData.DesktopWidth - 1,
		Bottom:              c.clientCoreData.DesktopHeight - 1,
	})

	c.Emit("ready")
}

func (c *Client) recvPDU(s []byte) {
	r := readerPool.Get().(*bytes.Reader)
	r.Reset(s)
	defer readerPool.Put(r)
	if r.Len() > 0 {
		p, err := readPDU(r)
		if err != nil {
			slog.Error("recvPDU", "err", err)
			return
		}
		if p.ShareCtrlHeader.PDUType == PDUTYPE_DEACTIVATEALLPDU {
			c.transport.Once("data", c.recvDemandActivePDU)
		} else if p.ShareCtrlHeader.PDUType == PDUTYPE_SERVER_REDIR_PKT {
			if redir, ok := p.Message.(*ServerRedirectionPDU); ok {
				c.Emit("redirect", redir)
			}
		} else if p.ShareCtrlHeader.PDUType == PDUTYPE_DATAPDU {
			d := p.Message.(*DataPDU)
			if d.Header.PDUType2 == PDUTYPE2_UPDATE {
				up := d.Data.(*UpdateDataPDU)
				p := up.Udata
				if up.UpdateType == FASTPATH_UPDATETYPE_BITMAP {
					c.Emit("bitmap", p.(*BitmapUpdateDataPDU).Rectangles)
				} else if up.UpdateType == FASTPATH_UPDATETYPE_ORDERS {
					c.Emit("orders", p.(*FastPathOrdersPDU).OrderPdus)
				}
			} else if d.Header.PDUType2 == PDUTYPE2_POINTER {
				pp := d.Data.(*PointerDataPDU)
				if pp.Pdata != nil {
					switch pp.MessageType {
					case TS_PTRUPDATE_TYPE_CACHED:
						c.Emit("pointer_cached", pp.Pdata.(*FastPathUpdateCachedPDU).CacheIdx)
					case TS_PTRUPDATE_TYPE_POINTER:
						c.Emit("pointer_update", pp.Pdata.(*FastPathUpdatePointerPDU))
					}
				}
				if pp.MessageType == TS_PTRUPDATE_TYPE_SYSTEM {
					c.Emit("pointer_hide")
				}
			}
		}
	}
}

func (c *Client) RecvFastPath(secFlag byte, s []byte) {
	r := readerPool.Get().(*bytes.Reader)
	r.Reset(s)
	defer readerPool.Put(r)
	for r.Len() > 0 {
		updateHeader, err := core.ReadUInt8(r)
		if err != nil {
			return
		}
		updateCode := updateHeader & 0x0f
		fragmentation := updateHeader & 0x30
		compression := updateHeader & 0xC0

		var compressionFlags uint8 = 0
		if compression == FASTPATH_OUTPUT_COMPRESSION_USED {
			compressionFlags, err = core.ReadUInt8(r)
		}

		size, err := core.ReadUint16LE(r)

		if err != nil {
			return
		}
		slog.Debug("RecvFastPath", "Code", FastPathUpdateType(updateCode),
			"compressionFlags", compressionFlags,
			"fragmentation", fragmentation,
			"size", size, "len", r.Len())
		if compressionFlags&RDP_MPPC_COMPRESSED != 0 {
			slog.Debug("RDP_MPPC_COMPRESSED")
		}
		if fragmentation != FASTPATH_FRAGMENT_SINGLE {
			if fragmentation == FASTPATH_FRAGMENT_FIRST {
				c.buff.Reset()
			}
			b, _ := core.ReadBytes(r.Len(), r)
			c.buff.Write(b)
			if fragmentation != FASTPATH_FRAGMENT_LAST {
				return
			}
			r.Reset(c.buff.Bytes())
		}

		// Surface Commands: parse directly (needs to know data size)
		if updateCode == FASTPATH_UPDATETYPE_SURFCMDS {
			var readLen int
			if fragmentation != FASTPATH_FRAGMENT_SINGLE {
				readLen = r.Len() // assembled: all data is this update
			} else {
				readLen = int(size) // single: read exactly our portion
			}
			surfData, err := core.ReadBytes(readLen, r)
			if err != nil {
				slog.Warn("RecvFastPath: failed to read SURFCMDS", "err", err)
				continue
			}
			result := ParseSurfaceCommands(surfData)
			if len(result.Rects) > 0 {
				c.Emit("bitmap", result.Rects)
			}
			for _, fid := range result.FrameIDs {
				c.sendDataPDU(&FrameAcknowledgeDataPDU{FrameID: fid})
			}
			continue
		}

		p, err := readFastPathUpdatePDU(r, updateCode)
		if err != nil {
			slog.Warn("readFastPathUpdatePDU:", "Code", FastPathUpdateType(updateCode), "err", err)
			return
		}

		if updateCode == FASTPATH_UPDATETYPE_BITMAP {
			c.Emit("bitmap", p.Data.(*FastPathBitmapUpdateDataPDU).Rectangles)
		} else if updateCode == FASTPATH_UPDATETYPE_COLOR {
			c.Emit("color", p.Data.(*FastPathColorPdu))
		} else if updateCode == FASTPATH_UPDATETYPE_ORDERS {
			c.Emit("orders", p.Data.(*FastPathOrdersPDU).OrderPdus)
		} else if updateCode == FASTPATH_UPDATETYPE_PTR_NULL {
			c.Emit("pointer_hide")
		} else if updateCode == FASTPATH_UPDATETYPE_CACHED {
			c.Emit("pointer_cached", p.Data.(*FastPathUpdateCachedPDU).CacheIdx)
		} else if updateCode == FASTPATH_UPDATETYPE_POINTER {
			c.Emit("pointer_update", p.Data.(*FastPathUpdatePointerPDU))
		}
	}
}

type InputEventsInterface interface {
	Serialize() []byte
}

// fastPathEncoder is implemented by input event types that know how to
// produce their Fast-Path Input wire encoding (MS-RDPBCGR §2.2.8.1.2.2).
type fastPathEncoder interface {
	FastPathEncode(buf []byte) []byte
}

func (c *Client) SendInputEvents(msgType uint16, events []InputEventsInterface) {
	if c.serverFastPathInput && c.fastPathSender != nil && c.canSendFastPathInput(events) {
		if c.sendFastPathInputEvents(events) {
			return
		}
		// Fall back to slow-path on send failure (e.g. legacy encryption).
	}

	p := &ClientInputEventPDU{}
	p.NumEvents = uint16(len(events))
	p.SlowPathInputEvents = make([]SlowPathInputEvent, 0, p.NumEvents)
	for _, in := range events {
		seria := in.Serialize()
		s := SlowPathInputEvent{0, msgType, len(seria), seria}
		p.SlowPathInputEvents = append(p.SlowPathInputEvents, s)
	}

	c.sendDataPDU(p)
}

// canSendFastPathInput reports whether every event in the batch implements
// the fast-path encoder.  Falls back to slow-path if any event type doesn't
// (currently just SynchronizeEvent, which the client never sends).
func (c *Client) canSendFastPathInput(events []InputEventsInterface) bool {
	if len(events) == 0 || len(events) > 15 {
		return false
	}
	for _, e := range events {
		if _, ok := e.(fastPathEncoder); !ok {
			return false
		}
	}
	return true
}

func (c *Client) sendFastPathInputEvents(events []InputEventsInterface) bool {
	// Worst case: 1 byte numberEvents + 7 bytes per event (mouse).
	buf := make([]byte, 0, 1+7*len(events))
	buf = append(buf, byte(len(events)))
	for _, e := range events {
		buf = e.(fastPathEncoder).FastPathEncode(buf)
	}
	if _, err := c.fastPathSender.SendFastPath(0, buf); err != nil {
		// Disable for the rest of the session so we don't keep paying the
		// failed-attempt cost on every input event.
		c.serverFastPathInput = false
		slog.Warn("fast-path input disabled, falling back to slow-path", "err", err)
		return false
	}
	return true
}

// SendRefreshRect requests the server to redraw the given screen rectangle.
// This causes the server to send a full refresh (including a new H.264 IDR)
// for the specified region, which is useful after a decoder reset.
func (c *Client) SendRefreshRect(width, height uint16) {
	slog.Debug("PDU: SendRefreshRect", "w", width, "h", height)
	c.sendDataPDU(&RefreshRectPDU{
		NumberOfAreas: 1,
		Right:         width - 1,
		Bottom:        height - 1,
	})
}

// SendForceRefresh asks the server for a complete display repaint by toggling
// SuppressOutput off→on.  Per MS-RDPBCGR 2.2.11.3.1, sending ALLOW_DISPLAY_UPDATES
// after SUPPRESS_DISPLAY_UPDATES forces the server to send a fresh full-screen
// update — for the RDPGFX H.264 pipeline this means a new IDR frame, which is
// what we need to recover after a hardware-decoder hard reset.  Plain
// SendRefreshRect is sometimes silently ignored by Windows servers while a
// video stream is active; this is the reliable fallback used by mstsc/FreeRDP.
func (c *Client) SendForceRefresh(width, height uint16) {
	slog.Debug("PDU: SendForceRefresh (suppress→allow)", "w", width, "h", height)
	c.sendDataPDU(&SuppressOutputPDU{
		AllowDisplayUpdates: 0x00, // SUPPRESS_DISPLAY_UPDATES
	})
	c.sendDataPDU(&SuppressOutputPDU{
		AllowDisplayUpdates: 0x01, // ALLOW_DISPLAY_UPDATES
		Right:               width - 1,
		Bottom:              height - 1,
	})
}
