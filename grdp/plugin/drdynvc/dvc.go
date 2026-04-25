package drdynvc

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/nakagami/grdp/core"
	"github.com/nakagami/grdp/plugin"
)

const (
	ChannelName   = plugin.DRDYNVC_SVC_CHANNEL_NAME
	ChannelOption = plugin.CHANNEL_OPTION_INITIALIZED |
		plugin.CHANNEL_OPTION_ENCRYPT_RDP
)

const (
	MAX_DVC_CHANNELS = 20
)

const (
	DYNVC_CREATE_REQ            = 0x01
	DYNVC_DATA_FIRST            = 0x02
	DYNVC_DATA                  = 0x03
	DYNVC_CLOSE                 = 0x04
	DYNVC_CAPABILITIES          = 0x05
	DYNVC_DATA_FIRST_COMPRESSED = 0x06
	DYNVC_DATA_COMPRESSED       = 0x07
	DYNVC_SOFT_SYNC_REQUEST     = 0x08
	DYNVC_SOFT_SYNC_RESPONSE    = 0x09
)

// DvcChannelHandler processes data for a specific dynamic virtual channel.
type DvcChannelHandler interface {
	Process(data []byte)
}

type ChannelClient struct {
	name          string
	id            uint32
	channelSender core.ChannelSender
}

type dvcChannelInfo struct {
	name    string
	id      uint32
	cbChId  uint8
	handler DvcChannelHandler
}

type dvcReassembly struct {
	buf      bytes.Buffer
	totalLen uint32
}

type DvcClient struct {
	w                 core.ChannelSender
	channels          map[string]ChannelClient
	handlers          map[string]DvcChannelHandler // channelName → handler
	rejectedChannels  map[string]bool              // channelName → explicitly rejected
	channelById       map[uint32]*dvcChannelInfo   // channelId → info
	reassembly        map[uint32]*dvcReassembly    // channelId → reassembly state
	negotiatedVersion uint16
}

func NewDvcClient() *DvcClient {
	return &DvcClient{
		channels:         make(map[string]ChannelClient, 100),
		handlers:         make(map[string]DvcChannelHandler),
		rejectedChannels: make(map[string]bool),
		channelById:      make(map[uint32]*dvcChannelInfo),
		reassembly:       make(map[uint32]*dvcReassembly),
	}
}

// RegisterHandler registers a handler for a named DVC channel.
func (c *DvcClient) RegisterHandler(name string, handler DvcChannelHandler) {
	c.handlers[name] = handler
}

// RegisterRejectedChannel marks a DVC channel to be explicitly rejected
// (non-zero CreationStatus) so the server does not use it.
// Use this to steer servers toward a fallback channel; for example,
// rejecting AUDIO_PLAYBACK_LOSSY_DVC forces gnome-remote-desktop to
// fall back to lossless AUDIO_PLAYBACK_DVC (PCM).
func (c *DvcClient) RegisterRejectedChannel(name string) {
	c.rejectedChannels[name] = true
}

func (c *DvcClient) LoadAddin(f core.ChannelSender) {

}

type DvcHeader struct {
	cmd    uint8
	sp     uint8
	cbChId uint8
}

func readHeader(r io.Reader) *DvcHeader {
	value, _ := core.ReadUInt8(r)
	cmd := (value & 0xf0) >> 4
	sp := (value & 0x0c) >> 2
	cbChId := (value & 0x03) >> 0
	return &DvcHeader{cmd, sp, cbChId}
}

func (h *DvcHeader) serialize(channelId uint32) []byte {
	b := &bytes.Buffer{}
	core.WriteUInt8((h.cmd<<4)|(h.sp<<2)|h.cbChId, b)
	if h.cbChId == 0 {
		core.WriteUInt8(uint8(channelId), b)
	} else if h.cbChId == 1 {
		core.WriteUInt16LE(uint16(channelId), b)
	} else {
		core.WriteUInt32LE(channelId, b)
	}

	return b.Bytes()
}

func (c *DvcClient) Send(s []byte) (int, error) {
	slog.Debug("dvc Send", "len", len(s), "data", hex.EncodeToString(s))
	name, _ := c.GetType()
	return c.w.SendToChannel(name, s)
}

// SendDvcData sends data on a DVC channel wrapped in a DYNVC_DATA PDU.
func (c *DvcClient) SendDvcData(channelId uint32, data []byte) {
	ch, ok := c.channelById[channelId]
	if !ok {
		return
	}
	hdr := &DvcHeader{cmd: DYNVC_DATA, sp: 0, cbChId: ch.cbChId}
	b := &bytes.Buffer{}
	b.Write(hdr.serialize(channelId))
	b.Write(data)
	c.Send(b.Bytes())
}
func (c *DvcClient) Sender(f core.ChannelSender) {
	c.w = f
}
func (c *DvcClient) GetType() (string, uint32) {
	return ChannelName, ChannelOption
}

func (c *DvcClient) Process(s []byte) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("dvc: panic in Process", "err", r)
		}
	}()
	r := bytes.NewReader(s)
	hdr := readHeader(r)
	slog.Debug(fmt.Sprintf("dvc: Cmd=0x%x, Sp=%d CbChId=%d remaining=%d", hdr.cmd, hdr.sp, hdr.cbChId, r.Len()))

	b, _ := core.ReadBytes(r.Len(), r)

	switch hdr.cmd {
	case DYNVC_CAPABILITIES:
		slog.Debug("DYNVC_CAPABILITIES")
		c.processCapsPdu(hdr, b)
	case DYNVC_CREATE_REQ:
		slog.Debug("DYNVC_CREATE_REQ")
		c.processCreateReq(hdr, b)
	case DYNVC_DATA_FIRST:
		c.processDataFirst(hdr, b)
	case DYNVC_DATA:
		c.processData(hdr, b)
	case DYNVC_CLOSE:
		c.processClose(hdr, b)
	case DYNVC_SOFT_SYNC_REQUEST:
		slog.Debug("DYNVC_SOFT_SYNC_REQUEST")
		c.processSoftSyncRequest(hdr, b)
	default:
		slog.Warn(fmt.Sprintf("dvc: unhandled cmd 0x%x", hdr.cmd))
	}
}
func (c *DvcClient) processClose(hdr *DvcHeader, s []byte) {
	r := bytes.NewReader(s)
	channelId := readDvcId(r, hdr.cbChId)
	ch, ok := c.channelById[channelId]
	name := "(unknown)"
	if ok {
		name = ch.name
		delete(c.channelById, channelId)
		delete(c.reassembly, channelId)
	}
	slog.Debug("dvc: CLOSE", "channelId", channelId, "name", name)
}

func (c *DvcClient) processCreateReq(hdr *DvcHeader, s []byte) {
	r := bytes.NewReader(s)
	channelId := readDvcId(r, hdr.cbChId)
	nameBytes, _ := core.ReadBytes(r.Len(), r)
	channelName := strings.TrimRight(string(nameBytes), "\x00")
	slog.Debug(fmt.Sprintf("Server requests channelId=%d, name=%s", channelId, channelName))

	// Associate handler if registered
	var handler DvcChannelHandler
	if h, ok := c.handlers[channelName]; ok {
		handler = h
		info := &dvcChannelInfo{
			name:    channelName,
			id:      channelId,
			cbChId:  hdr.cbChId,
			handler: handler,
		}
		c.channelById[channelId] = info

		// Provide send callback if handler supports it
		if setter, ok := handler.(interface{ SetSendFunc(func([]byte)) }); ok {
			chId := channelId
			setter.SetSendFunc(func(data []byte) {
				c.SendDvcData(chId, data)
			})
		}
		slog.Debug(fmt.Sprintf("dvc: handler registered for channel %s (id=%d)", channelName, channelId))
	}

	// If explicitly rejected, send a non-zero CreationStatus so the server
	// does not use this channel (e.g. AUDIO_PLAYBACK_LOSSY_DVC → fallback to PCM).
	if c.rejectedChannels[channelName] {
		slog.Debug(fmt.Sprintf("dvc: rejecting channel %s (id=%d)", channelName, channelId))
		rspHdr := &DvcHeader{cmd: DYNVC_CREATE_REQ, sp: 0, cbChId: hdr.cbChId}
		b := &bytes.Buffer{}
		b.Write(rspHdr.serialize(channelId))
		core.WriteUInt32LE(0x80004005, b) // E_FAIL
		c.Send(b.Bytes())
		return
	}

	// Send success response (Sp SHOULD be 0 per MS-RDPEDYC 2.2.2.2).
	// Always accept: some Windows servers stop sending data on static
	// virtual channels (e.g. cliprdr) when DVC creation requests are
	// rejected, even for unrelated channels.
	rspHdr := &DvcHeader{cmd: DYNVC_CREATE_REQ, sp: 0, cbChId: hdr.cbChId}
	b := &bytes.Buffer{}
	b.Write(rspHdr.serialize(channelId))
	core.WriteUInt32LE(0, b)
	c.Send(b.Bytes())

	// Notify handler that channel is ready (CREATE_RSP has been sent)
	if handler != nil {
		if ch, ok := handler.(interface{ OnChannelCreated() }); ok {
			ch.OnChannelCreated()
		}
	}
}

func readDvcId(r io.Reader, cbLen uint8) (id uint32) {
	switch cbLen {
	case 0:
		i, _ := core.ReadUInt8(r)
		id = uint32(i)
	case 1:
		i, _ := core.ReadUint16LE(r)
		id = uint32(i)
	default:
		id, _ = core.ReadUInt32LE(r)
	}
	return
}
func (c *DvcClient) processDataFirst(hdr *DvcHeader, s []byte) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("dvc: panic in processDataFirst", "err", r)
		}
	}()
	r := bytes.NewReader(s)
	channelId := readDvcId(r, hdr.cbChId)

	// Read total length (encoding based on sp/Len field)
	var totalLen uint32
	switch hdr.sp {
	case 0:
		l, _ := core.ReadUInt8(r)
		totalLen = uint32(l)
	case 1:
		l, _ := core.ReadUint16LE(r)
		totalLen = uint32(l)
	default:
		totalLen, _ = core.ReadUInt32LE(r)
	}

	data, _ := core.ReadBytes(r.Len(), r)
	slog.Debug(fmt.Sprintf("dvc DATA_FIRST ch=%d totalLen=%d chunkLen=%d", channelId, totalLen, len(data)))

	ch, ok := c.channelById[channelId]
	if !ok || ch.handler == nil {
		return
	}

	if uint32(len(data)) >= totalLen {
		ch.handler.Process(data[:totalLen])
	} else {
		ra := &dvcReassembly{totalLen: totalLen}
		ra.buf.Write(data)
		c.reassembly[channelId] = ra
	}
}

func (c *DvcClient) processData(hdr *DvcHeader, s []byte) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("dvc: panic in processData", "err", r)
		}
	}()
	r := bytes.NewReader(s)
	channelId := readDvcId(r, hdr.cbChId)
	data, _ := core.ReadBytes(r.Len(), r)

	ch, ok := c.channelById[channelId]
	if !ok || ch.handler == nil {
		return
	}

	ra, hasReassembly := c.reassembly[channelId]
	if hasReassembly {
		ra.buf.Write(data)
		if uint32(ra.buf.Len()) >= ra.totalLen {
			ch.handler.Process(ra.buf.Bytes()[:ra.totalLen])
			delete(c.reassembly, channelId)
		}
	} else {
		ch.handler.Process(data)
	}
}

func (c *DvcClient) processCapsPdu(hdr *DvcHeader, s []byte) {
	r := bytes.NewReader(s)
	core.ReadUInt8(r)
	ver, _ := core.ReadUint16LE(r)
	slog.Debug(fmt.Sprintf("Server supports dvc=%d", ver))

	// Respond with the server's version (up to 3).
	// Version 3 is required for some servers to activate RDPGFX.
	if ver > 3 {
		ver = 3
	}

	// Client CAPS response: header(1) + pad(1) + version(2) = 4 bytes
	// Priority charges are only in the server's CAPS request, not the client response.
	b := &bytes.Buffer{}
	core.WriteUInt8(0x50, b) // header: Cmd=5(CAPS), Sp=0, CbChId=0
	core.WriteUInt8(0x00, b) // pad
	core.WriteUInt16LE(ver, b)
	slog.Debug("dvc: CAPS response", "version", ver, "len", b.Len())
	c.Send(b.Bytes())
	c.negotiatedVersion = ver
}

func (c *DvcClient) processSoftSyncRequest(hdr *DvcHeader, s []byte) {
	r := bytes.NewReader(s)
	core.ReadUInt8(r)        // Pad
	length, _ := core.ReadUInt32LE(r) // Length
	flags, _ := core.ReadUint16LE(r)  // Flags
	numTunnels, _ := core.ReadUint16LE(r)
	slog.Debug("DYNVC_SOFT_SYNC_REQUEST", "length", length, "flags", flags, "numTunnels", numTunnels)

	// Send SOFT_SYNC_RESPONSE: header + pad + length(4)
	b := &bytes.Buffer{}
	core.WriteUInt8((DYNVC_SOFT_SYNC_RESPONSE<<4)|0x00, b) // cmd=9, sp=0, cbChId=0
	core.WriteUInt8(0, b)          // Pad
	core.WriteUInt32LE(0x04, b)    // Length = 4 (just the length field)
	c.Send(b.Bytes())
}
