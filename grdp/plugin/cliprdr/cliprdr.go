package cliprdr

import (
	"bytes"
	"fmt"
	"log/slog"
	"unicode/utf16"

	"github.com/lunixbochs/struc"

	"github.com/nakagami/grdp/core"
)

type CliprdrClient struct {
	w                     core.ChannelSender
	useLongFormatNames    bool
	streamFileClipEnabled bool
	fileClipNoFilePaths   bool
	canLockClipData       bool
	hasHugeFileSupport    bool
	formatIdMap           map[uint32]uint32
	reply                 chan []byte
}

func NewCliprdrClient() *CliprdrClient {
	c := &CliprdrClient{
		formatIdMap: make(map[uint32]uint32, 20),
		reply:       make(chan []byte, 100),
	}

	go ClipWatcher(c)

	return c
}

func (c *CliprdrClient) Sender(f core.ChannelSender) {
	c.w = f
}
func (c *CliprdrClient) GetType() (string, uint32) {
	return ChannelName, ChannelOption
}

func (c *CliprdrClient) Process(s []byte) {
	r := bytes.NewReader(s)

	msgType, _ := core.ReadUint16LE(r)
	flag, _ := core.ReadUint16LE(r)
	length, _ := core.ReadUInt32LE(r)
	slog.Debug(fmt.Sprintf("cliprdr: type=0x%x flag=%d length=%d, all=%d", msgType, flag, length, r.Len()))

	b, _ := core.ReadBytes(int(length), r)

	switch msgType {
	case CB_CLIP_CAPS:
		slog.Debug("CB_CLIP_CAPS")
		c.processClipCaps(b)

	case CB_MONITOR_READY:
		slog.Debug("CB_MONITOR_READY")
		c.processMonitorReady(b)

	case CB_FORMAT_LIST:
		slog.Debug("CB_FORMAT_LIST")
		c.processFormatList(b)

	case CB_FORMAT_LIST_RESPONSE:
		slog.Debug("CB_FORMAT_LIST_RESPONSE")
		c.processFormatListResponse(flag, b)

	case CB_FORMAT_DATA_REQUEST:
		slog.Debug("CB_FORMAT_DATA_REQUEST")
		c.processFormatDataRequest(b)

	case CB_FORMAT_DATA_RESPONSE:
		slog.Debug("CB_FORMAT_DATA_RESPONSE")
		c.processFormatDataResponse(flag, b)

	case CB_FILECONTENTS_REQUEST:
		slog.Debug("CB_FILECONTENTS_REQUEST")
		c.processFileContentsRequest(b)

	case CB_FILECONTENTS_RESPONSE:
		slog.Debug("CB_FILECONTENTS_RESPONSE")
		c.processFileContentsResponse(flag, b)

	case CB_LOCK_CLIPDATA:
		slog.Debug("CB_LOCK_CLIPDATA")
		c.processLockClipData(b)

	case CB_UNLOCK_CLIPDATA:
		slog.Debug("CB_UNLOCK_CLIPDATA")
		c.processUnlockClipData(b)

	default:
		slog.Error(fmt.Sprintf("type 0x%x not supported", msgType))
	}
}
func (c *CliprdrClient) processClipCaps(b []byte) {
	r := bytes.NewReader(b)
	var cp CliprdrCapabilitiesPDU
	err := struc.Unpack(r, &cp)
	if err != nil {
		slog.Error("Failed to unpack", "error", err)
		return
	}
	slog.Debug(fmt.Sprintf("Capabilities:%+v", cp))
	c.useLongFormatNames = cp.CapabilitySets[0].GeneralFlags&CB_USE_LONG_FORMAT_NAMES != 0
	c.streamFileClipEnabled = cp.CapabilitySets[0].GeneralFlags&CB_STREAM_FILECLIP_ENABLED != 0
	c.fileClipNoFilePaths = cp.CapabilitySets[0].GeneralFlags&CB_FILECLIP_NO_FILE_PATHS != 0
	c.canLockClipData = cp.CapabilitySets[0].GeneralFlags&CB_CAN_LOCK_CLIPDATA != 0
	c.hasHugeFileSupport = cp.CapabilitySets[0].GeneralFlags&CB_HUGE_FILE_SUPPORT_ENABLED != 0
	slog.Debug("UseLongFormatNames", "value", c.useLongFormatNames)
	slog.Debug("StreamFileClipEnabled", "value", c.streamFileClipEnabled)
	slog.Debug("FileClipNoFilePaths", "value", c.fileClipNoFilePaths)
	slog.Debug("CanLockClipData", "value", c.canLockClipData)
	slog.Debug("HasHugeFileSupport", "value", c.hasHugeFileSupport)
}

func (c *CliprdrClient) processMonitorReady(b []byte) {
	c.sendClientCapabilitiesPDU()
	c.sendFormatListPDU()
}

func (c *CliprdrClient) processFormatList(b []byte) {
	EmptyClipboard()
	fl, _ := c.readForamtList(b)
	slog.Debug("numFormats", "count", fl.NumFormats)
	c.sendFormatListResponse(CB_RESPONSE_OK)
}

func (c *CliprdrClient) processFormatListResponse(flag uint16, b []byte) {
	if flag != CB_RESPONSE_OK {
		slog.Error("Format List Response Failed")
		return
	}
	slog.Debug("Format List Response OK")
}

func (c *CliprdrClient) processFormatDataRequest(b []byte) {
	r := bytes.NewReader(b)
	_, _ = core.ReadUInt32LE(r) // requestId

	buff := &bytes.Buffer{}
	// Text-only: directly get clipboard data for any text format
	data := GetClipboardText()
	slog.Debug("clipboard data", "content", data)
	buff.Write(core.UnicodeEncode(data))
	buff.Write([]byte{0, 0})

	c.sendFormatDataResponse(buff.Bytes())
}
func (c *CliprdrClient) processFormatDataResponse(flag uint16, b []byte) {
	if flag != CB_RESPONSE_OK {
		slog.Error("Format Data Response Failed")
	}
	c.reply <- b
}

func (c *CliprdrClient) processFileContentsRequest(b []byte) {
	// Text-only mode doesn't support file transfer
	slog.Debug("File transfer not supported in text-only mode")
}

func (c *CliprdrClient) processFileContentsResponse(flag uint16, b []byte) {
	// Text-only mode doesn't support file transfer
}
func (c *CliprdrClient) processLockClipData(b []byte) {
	r := bytes.NewReader(b)
	var l CliprdrCtrlClipboardData
	l.ClipDataId, _ = core.ReadUInt32LE(r)
}
func (c *CliprdrClient) processUnlockClipData(b []byte) {
	r := bytes.NewReader(b)
	var l CliprdrCtrlClipboardData
	l.ClipDataId, _ = core.ReadUInt32LE(r)

}

func (c *CliprdrClient) sendClientCapabilitiesPDU() {
	slog.Debug("Send Client Clipboard Capabilities PDU (text-only mode)")
	var cs CliprdrGeneralCapabilitySet
	cs.CapabilitySetLength = 12
	cs.CapabilitySetType = CB_CAPSTYPE_GENERAL
	cs.Version = CB_CAPS_VERSION_2
	// Text-only mode: only use long format names
	cs.GeneralFlags = CB_USE_LONG_FORMAT_NAMES
	body := &bytes.Buffer{}
	core.WriteUInt16LE(1, body) // cCapabilitiesSets
	core.WriteUInt16LE(0, body) // pad
	struc.Pack(body, cs)
	sendClipPDU(c.w, CB_CLIP_CAPS, 0, body.Bytes())
}

func (c *CliprdrClient) sendTemporaryDirectoryPDU() {
	slog.Debug("Send Temporary Directory PDU (ignored in text-only mode)")
}

func (c *CliprdrClient) sendFormatListPDU() {
	slog.Debug("Send Format List PDU (text formats only)")
	formats := GetFormatList()
	slog.Debug("available formats", "count", len(formats), "formats", formats)

	body := &bytes.Buffer{}
	for _, v := range formats {
		core.WriteUInt32LE(v.FormatId, body)
		if v.FormatName == "" {
			core.WriteUInt16LE(0, body)
		} else {
			n := core.UnicodeEncode(v.FormatName)
			core.WriteBytes(n, body)
			body.Write([]byte{0, 0})
		}
	}
	sendClipPDU(c.w, CB_FORMAT_LIST, 0, body.Bytes())
}

func (c *CliprdrClient) readForamtList(b []byte) (*CliprdrFormatList, bool) {
	r := bytes.NewReader(b)
	fs := make([]CliprdrFormat, 0, 20)
	var numFormats uint32 = 0
	c.formatIdMap = make(map[uint32]uint32, 0)
	for r.Len() > 0 {
		foramtId, _ := core.ReadUInt32LE(r)
		bs := make([]uint16, 0, 20)
		ln := r.Len()
		for j := 0; j < ln; j++ {
			b, _ := core.ReadUint16LE(r)
			if b == 0 {
				break
			}
			bs = append(bs, b)
		}
		name := string(utf16.Decode(bs))
		slog.Debug(fmt.Sprintf("Format:%d Name:<%s>", foramtId, name))
		if name != "" {
			localId := RegisterClipboardFormat(name)
			slog.Debug("format mapping", "local", localId, "remote", foramtId)
			c.formatIdMap[localId] = foramtId
		} else {
			c.formatIdMap[foramtId] = foramtId
		}

		numFormats++
		fs = append(fs, CliprdrFormat{foramtId, name})
	}

	return &CliprdrFormatList{numFormats, fs}, false
}

func (c *CliprdrClient) sendFormatListResponse(flags uint16) {
	slog.Debug("Send Format List Response")
	sendClipPDU(c.w, CB_FORMAT_LIST_RESPONSE, flags, nil)
}

func (c *CliprdrClient) sendFormatDataRequest(id uint32) {
	slog.Debug("Send Format Data Request")
	body := &bytes.Buffer{}
	core.WriteUInt32LE(id, body)
	sendClipPDU(c.w, CB_FORMAT_DATA_REQUEST, 0, body.Bytes())
}

func (c *CliprdrClient) sendFormatDataResponse(b []byte) {
	slog.Debug("Send Format Data Response")
	sendClipPDU(c.w, CB_FORMAT_DATA_RESPONSE, CB_RESPONSE_OK, b)
}
