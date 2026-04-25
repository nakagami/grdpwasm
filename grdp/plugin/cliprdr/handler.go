// Package cliprdr handler.go implements a cross-platform CLIPRDR
// (Clipboard Virtual Channel Extension, MS-RDPECLIP) handler for
// bidirectional text clipboard sharing between RDP client and server.
//
// Only text formats (CF_UNICODETEXT / CF_TEXT) are supported.
package cliprdr

import (
	"bytes"
	"encoding/binary"
	"log/slog"
	"strings"
	"unicode/utf16"

	"github.com/nakagami/grdp/core"
)

// CliprdrHandler implements plugin.ChannelTransport for the "cliprdr"
// static virtual channel.  It uses callbacks for clipboard integration
// so that any UI toolkit can wire in its own clipboard access.
type CliprdrHandler struct {
	channelSender core.ChannelSender

	useLongFormatNames bool

	// onRemoteClipboardChanged is called with the text when the server's
	// clipboard content arrives.
	onRemoteClipboardChanged func(text string)

	// getLocalClipboardText is called to retrieve the current local
	// clipboard text when the server requests it.
	getLocalClipboardText func() string

	// suppressNextLocalChange prevents an echo loop:
	// server→client clipboard update triggers a local clipboard change
	// event which would otherwise be sent back to the server.
	suppressNextLocalChange bool
}

// NewHandler creates a CliprdrHandler.
//
//   - onRemote is called when the server clipboard text is received.
//   - getLocal is called to retrieve the current local clipboard text.
//
// Either callback may be nil.
func NewHandler(onRemote func(text string), getLocal func() string) *CliprdrHandler {
	return &CliprdrHandler{
		onRemoteClipboardChanged: onRemote,
		getLocalClipboardText:    getLocal,
	}
}

// --- plugin.ChannelTransport interface ------------------------------------

func (h *CliprdrHandler) GetType() (string, uint32) {
	return ChannelName, ChannelOption
}

func (h *CliprdrHandler) Sender(f core.ChannelSender) {
	h.channelSender = f
}

// Process handles a reassembled CLIPRDR PDU from the server.
func (h *CliprdrHandler) Process(s []byte) {
	if len(s) < 8 {
		return
	}
	r := bytes.NewReader(s)
	msgType, _ := core.ReadUint16LE(r)
	msgFlags, _ := core.ReadUint16LE(r)
	dataLen, _ := core.ReadUInt32LE(r)

	body := make([]byte, dataLen)
	if dataLen > 0 {
		n, _ := r.Read(body)
		body = body[:n]
	}

	slog.Debug("cliprdr recv", "msgType", msgType, "msgFlags", msgFlags, "dataLen", dataLen)

	switch msgType {
	case CB_CLIP_CAPS:
		h.processClipCaps(body)
	case CB_MONITOR_READY:
		h.processMonitorReady()
	case CB_FORMAT_LIST:
		h.processFormatList(body, msgFlags)
	case CB_FORMAT_LIST_RESPONSE:
		h.processFormatListResponse(msgFlags)
	case CB_FORMAT_DATA_REQUEST:
		h.processFormatDataRequest(body)
	case CB_FORMAT_DATA_RESPONSE:
		h.processFormatDataResponse(body, msgFlags)
	case CB_LOCK_CLIPDATA, CB_UNLOCK_CLIPDATA:
		// ignored
	default:
		slog.Debug("cliprdr: unhandled msgType", "msgType", msgType)
	}
}

// --- Clipboard Capabilities (MS-RDPECLIP 2.2.2.1) -------------------------

func (h *CliprdrHandler) processClipCaps(body []byte) {
	if len(body) < 4 {
		return
	}
	cCapSets := binary.LittleEndian.Uint16(body[0:2])
	// pad1 at [2:4]
	offset := 4
	for i := 0; i < int(cCapSets); i++ {
		if offset+4 > len(body) {
			break
		}
		capType := binary.LittleEndian.Uint16(body[offset:])
		capLen := binary.LittleEndian.Uint16(body[offset+2:])
		if capType == CB_CAPSTYPE_GENERAL && capLen >= 12 {
			generalFlags := binary.LittleEndian.Uint32(body[offset+8:])
			h.useLongFormatNames = generalFlags&CB_USE_LONG_FORMAT_NAMES != 0
			slog.Debug("cliprdr: server caps", "generalFlags", generalFlags, "longNames", h.useLongFormatNames)
		}
		offset += int(capLen)
	}
}

func (h *CliprdrHandler) sendClipCaps() {
	b := &bytes.Buffer{}
	// General capability set: type(2) + length(2) + version(4) + flags(4)
	binary.Write(b, binary.LittleEndian, uint16(CB_CAPSTYPE_GENERAL))
	binary.Write(b, binary.LittleEndian, uint16(12))
	binary.Write(b, binary.LittleEndian, uint32(CB_CAPS_VERSION_2))
	binary.Write(b, binary.LittleEndian, uint32(CB_USE_LONG_FORMAT_NAMES))

	// cCapabilitySets(2) + pad1(2) + capabilitySet
	body := &bytes.Buffer{}
	binary.Write(body, binary.LittleEndian, uint16(1))
	binary.Write(body, binary.LittleEndian, uint16(0))
	body.Write(b.Bytes())

	h.sendPDU(CB_CLIP_CAPS, 0, body.Bytes())
}

// --- Monitor Ready (MS-RDPECLIP 2.2.2.2) ----------------------------------

func (h *CliprdrHandler) processMonitorReady() {
	slog.Debug("cliprdr: server Monitor Ready")
	h.sendClipCaps()
	h.sendFormatList()
}

// --- Format List (MS-RDPECLIP 2.2.3.1) ------------------------------------

func (h *CliprdrHandler) sendFormatList() {
	b := &bytes.Buffer{}
	if h.useLongFormatNames {
		// Long Format Name: formatId(4) + wszFormatName(null-terminated UTF-16LE)
		binary.Write(b, binary.LittleEndian, uint32(CF_UNICODETEXT))
		b.Write([]byte{0, 0}) // empty name = standard format
	} else {
		// Short Format Name: formatId(4) + formatName[32]
		binary.Write(b, binary.LittleEndian, uint32(CF_UNICODETEXT))
		b.Write(make([]byte, 32))
	}
	h.sendPDU(CB_FORMAT_LIST, 0, b.Bytes())
}

func (h *CliprdrHandler) processFormatList(body []byte, msgFlags uint16) {
	formats := h.parseFormatList(body, msgFlags)
	slog.Debug("cliprdr: server Format List", "formats", formats)

	// Always respond OK
	h.sendPDU(CB_FORMAT_LIST_RESPONSE, CB_RESPONSE_OK, nil)

	// Request text data if available
	for _, f := range formats {
		if f.FormatId == CF_UNICODETEXT {
			h.sendFormatDataRequest(CF_UNICODETEXT)
			return
		}
	}
	for _, f := range formats {
		if f.FormatId == CF_TEXT {
			h.sendFormatDataRequest(CF_TEXT)
			return
		}
	}
}

func (h *CliprdrHandler) parseFormatList(body []byte, msgFlags uint16) []CliprdrFormat {
	var formats []CliprdrFormat
	if h.useLongFormatNames && (msgFlags&CB_ASCII_NAMES == 0) {
		// Long Format Names (MS-RDPECLIP 2.2.3.1.1.1)
		offset := 0
		for offset+4 <= len(body) {
			fmtId := binary.LittleEndian.Uint32(body[offset:])
			offset += 4
			// Read null-terminated UTF-16LE string
			nameEnd := offset
			for nameEnd+1 < len(body) {
				if body[nameEnd] == 0 && body[nameEnd+1] == 0 {
					break
				}
				nameEnd += 2
			}
			name := decodeUTF16LE(body[offset:nameEnd])
			offset = nameEnd + 2
			formats = append(formats, CliprdrFormat{fmtId, name})
		}
	} else {
		// Short Format Names (MS-RDPECLIP 2.2.3.1.1.2)
		offset := 0
		for offset+36 <= len(body) {
			fmtId := binary.LittleEndian.Uint32(body[offset:])
			nameBytes := body[offset+4 : offset+36]
			var name string
			if msgFlags&CB_ASCII_NAMES != 0 {
				name = strings.TrimRight(string(nameBytes), "\x00")
			} else {
				name = decodeUTF16LE(nameBytes)
				name = strings.TrimRight(name, "\x00")
			}
			formats = append(formats, CliprdrFormat{fmtId, name})
			offset += 36
		}
	}
	return formats
}

func (h *CliprdrHandler) processFormatListResponse(msgFlags uint16) {
	if msgFlags&CB_RESPONSE_OK != 0 {
		slog.Debug("cliprdr: Format List Response OK")
	} else {
		slog.Warn("cliprdr: Format List Response FAIL")
	}
}

// --- Format Data Request / Response (MS-RDPECLIP 2.2.5) --------------------

func (h *CliprdrHandler) sendFormatDataRequest(formatId uint32) {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, formatId)
	h.sendPDU(CB_FORMAT_DATA_REQUEST, 0, b)
	slog.Debug("cliprdr: sent Format Data Request", "formatId", formatId)
}

func (h *CliprdrHandler) processFormatDataRequest(body []byte) {
	if len(body) < 4 {
		h.sendPDU(CB_FORMAT_DATA_RESPONSE, CB_RESPONSE_FAIL, nil)
		return
	}
	requestedFormat := binary.LittleEndian.Uint32(body[0:4])
	slog.Debug("cliprdr: server requests format", "formatId", requestedFormat)

	text := ""
	if h.getLocalClipboardText != nil {
		text = h.getLocalClipboardText()
	}

	switch requestedFormat {
	case CF_UNICODETEXT:
		encoded := encodeUTF16LE(text + "\x00")
		h.sendPDU(CB_FORMAT_DATA_RESPONSE, CB_RESPONSE_OK, encoded)
	case CF_TEXT:
		h.sendPDU(CB_FORMAT_DATA_RESPONSE, CB_RESPONSE_OK, []byte(text+"\x00"))
	default:
		h.sendPDU(CB_FORMAT_DATA_RESPONSE, CB_RESPONSE_FAIL, nil)
	}
}

func (h *CliprdrHandler) processFormatDataResponse(body []byte, msgFlags uint16) {
	if msgFlags&CB_RESPONSE_OK == 0 {
		slog.Warn("cliprdr: Format Data Response FAIL")
		return
	}

	// Try to decode as UTF-16LE (CF_UNICODETEXT)
	text := decodeUTF16LE(body)
	text = strings.TrimRight(text, "\x00")

	if text != "" && h.onRemoteClipboardChanged != nil {
		slog.Debug("cliprdr: received text", "len", len(text))
		h.suppressNextLocalChange = true
		h.onRemoteClipboardChanged(text)
	}
}

// --- Public API for local clipboard changes --------------------------------

// OnLocalClipboardChanged notifies the server that the local clipboard
// content has changed.  Call this from the UI when the system clipboard
// changes (e.g. via polling or a platform clipboard-change signal).
func (h *CliprdrHandler) OnLocalClipboardChanged() {
	if h.suppressNextLocalChange {
		h.suppressNextLocalChange = false
		return
	}
	if h.channelSender != nil {
		h.sendFormatList()
		slog.Debug("cliprdr: local clipboard changed, sent Format List")
	}
}

// --- Send helpers ----------------------------------------------------------

func (h *CliprdrHandler) sendPDU(msgType, msgFlags uint16, body []byte) {
	if h.channelSender == nil {
		return
	}
	sendClipPDU(h.channelSender, msgType, msgFlags, body)
}

// --- UTF-16LE helpers ------------------------------------------------------

func decodeUTF16LE(b []byte) string {
	if len(b) < 2 {
		return ""
	}
	// Trim to even length
	if len(b)%2 != 0 {
		b = b[:len(b)-1]
	}
	u16 := make([]uint16, len(b)/2)
	for i := range u16 {
		u16[i] = binary.LittleEndian.Uint16(b[i*2:])
	}
	return string(utf16.Decode(u16))
}

func encodeUTF16LE(s string) []byte {
	runes := []rune(s)
	u16 := utf16.Encode(runes)
	b := make([]byte, len(u16)*2)
	for i, v := range u16 {
		binary.LittleEndian.PutUint16(b[i*2:], v)
	}
	return b
}
