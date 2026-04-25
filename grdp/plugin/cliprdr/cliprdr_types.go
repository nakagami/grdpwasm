package cliprdr

import (
	"bytes"

	"github.com/lunixbochs/struc"

	"github.com/nakagami/grdp/core"
	"github.com/nakagami/grdp/plugin"
)

/**
 *                                    Initialization Sequence\n
 *     Client                                                                    Server\n
 *        |                                                                         |\n
 *        |<----------------------Server Clipboard Capabilities PDU-----------------|\n
 *        |<-----------------------------Monitor Ready PDU--------------------------|\n
 *        |-----------------------Client Clipboard Capabilities PDU---------------->|\n
 *        |---------------------------Temporary Directory PDU---------------------->|\n
 *        |-------------------------------Format List PDU-------------------------->|\n
 *        |<--------------------------Format List Response PDU----------------------|\n
 *
 */

/**
 *                                    Data Transfer Sequences\n
 *     Shared                                                                     Local\n
 *  Clipboard Owner                                                           Clipboard Owner\n
 *        |                                                                         |\n
 *        |-------------------------------------------------------------------------|\n _
 *        |-------------------------------Format List PDU-------------------------->|\n  |
 *        |<--------------------------Format List Response PDU----------------------|\n _| Copy
 * Sequence
 *        |<---------------------Lock Clipboard Data PDU (Optional)-----------------|\n
 *        |-------------------------------------------------------------------------|\n
 *        |-------------------------------------------------------------------------|\n _
 *        |<--------------------------Format Data Request PDU-----------------------|\n  | Paste
 * Sequence Palette,
 *        |---------------------------Format Data Response PDU--------------------->|\n _| Metafile,
 * File List Data
 *        |-------------------------------------------------------------------------|\n
 *        |-------------------------------------------------------------------------|\n _
 *        |<------------------------Format Contents Request PDU---------------------|\n  | Paste
 * Sequence
 *        |-------------------------Format Contents Response PDU------------------->|\n _| File
 * Stream Data
 *        |<---------------------Lock Clipboard Data PDU (Optional)-----------------|\n
 *        |-------------------------------------------------------------------------|\n
 *
 */

const (
	ChannelName   = plugin.CLIPRDR_SVC_CHANNEL_NAME
	ChannelOption = plugin.CHANNEL_OPTION_INITIALIZED | plugin.CHANNEL_OPTION_ENCRYPT_RDP |
		plugin.CHANNEL_OPTION_COMPRESS_RDP | plugin.CHANNEL_OPTION_SHOW_PROTOCOL
)

type MsgType uint16

const (
	CB_MONITOR_READY         = 0x0001
	CB_FORMAT_LIST           = 0x0002
	CB_FORMAT_LIST_RESPONSE  = 0x0003
	CB_FORMAT_DATA_REQUEST   = 0x0004
	CB_FORMAT_DATA_RESPONSE  = 0x0005
	CB_TEMP_DIRECTORY        = 0x0006
	CB_CLIP_CAPS             = 0x0007
	CB_FILECONTENTS_REQUEST  = 0x0008
	CB_FILECONTENTS_RESPONSE = 0x0009
	CB_LOCK_CLIPDATA         = 0x000A
	CB_UNLOCK_CLIPDATA       = 0x000B
)

type MsgFlags uint16

const (
	CB_RESPONSE_OK   = 0x0001
	CB_RESPONSE_FAIL = 0x0002
	CB_ASCII_NAMES   = 0x0004
)

type DwFlags uint32

const (
	FILECONTENTS_SIZE  = 0x00000001
	FILECONTENTS_RANGE = 0x00000002
)

type CliprdrPDUHeader struct {
	MsgType  uint16 `struc:"little"`
	MsgFlags uint16 `struc:"little"`
	DataLen  uint32 `struc:"little"`
}

func NewCliprdrPDUHeader(mType, flags uint16, ln uint32) *CliprdrPDUHeader {
	return &CliprdrPDUHeader{
		MsgType:  mType,
		MsgFlags: flags,
		DataLen:  ln,
	}
}
func (h *CliprdrPDUHeader) serialize() []byte {
	b := &bytes.Buffer{}
	core.WriteUInt16LE(h.MsgType, b)
	core.WriteUInt16LE(h.MsgFlags, b)
	core.WriteUInt32LE(h.DataLen, b)
	return b.Bytes()
}

type CliprdrGeneralCapabilitySet struct {
	CapabilitySetType   uint16 `struc:"little"`
	CapabilitySetLength uint16 `struc:"little"`
	Version             uint32 `struc:"little"`
	GeneralFlags        uint32 `struc:"little"`
}

const (
	CB_CAPSTYPE_GENERAL = 0x0001
)

type CliprdrCapabilitySets struct {
	CapabilitySetType uint16 `struc:"little"`
	LengthCapability  uint16 `struc:"little"`
	Version           uint32 `struc:"little"`
	GeneralFlags      uint32 `struc:"little"`
}
type CliprdrCapabilitiesPDU struct {
	CCapabilitiesSets uint16                        `struc:"little,sizeof=CapabilitySets"`
	Pad1              uint16                        `struc:"little"`
	CapabilitySets    []CliprdrGeneralCapabilitySet `struc:"little"`
}

type CliprdrMonitorReady struct {
}

type GeneralFlags uint32

const (
	CB_USE_LONG_FORMAT_NAMES     = 0x00000002
	CB_STREAM_FILECLIP_ENABLED   = 0x00000004
	CB_FILECLIP_NO_FILE_PATHS    = 0x00000008
	CB_CAN_LOCK_CLIPDATA         = 0x00000010
	CB_HUGE_FILE_SUPPORT_ENABLED = 0x00000020
)

const (
	CB_CAPS_VERSION_1 = 0x00000001
	CB_CAPS_VERSION_2 = 0x00000002
)
const (
	CB_CAPSTYPE_GENERAL_LEN = 12
)

const (
	FD_CLSID      = 0x00000001
	FD_SIZEPOINT  = 0x00000002
	FD_ATTRIBUTES = 0x00000004
	FD_CREATETIME = 0x00000008
	FD_ACCESSTIME = 0x00000010
	FD_WRITESTIME = 0x00000020
	FD_FILESIZE   = 0x00000040
	FD_PROGRESSUI = 0x00004000
	FD_LINKUI     = 0x00008000
)

const (
	FILE_ATTRIBUTE_DIRECTORY = 0x00000010
)

type FileGroupDescriptor struct {
	CItems uint32           `struc:"little"`
	Fgd    []FileDescriptor `struc:"sizefrom=CItems"`
}
type FileDescriptor struct {
	Flags          uint32   `struc:"little"`
	Clsid          [16]byte `struc:"little"`
	Sizel          [8]byte  `struc:"little"`
	Pointl         [8]byte  `struc:"little"`
	FileAttributes uint32   `struc:"little"`
	CreationTime   [8]byte  `struc:"little"`
	LastAccessTime [8]byte  `struc:"little"`
	LastWriteTime  []byte   `struc:"[8]byte"` //8
	FileSizeHigh   uint32   `struc:"little"`
	FileSizeLow    uint32   `struc:"little"`
	FileName       []byte   `struc:"[512]byte"`
}

func (f *FileGroupDescriptor) Unpack(b []byte) error {
	r := bytes.NewReader(b)
	return struc.Unpack(r, f)
}

func (f *FileDescriptor) serialize() []byte {
	b := &bytes.Buffer{}
	core.WriteUInt32LE(f.Flags, b)
	for i := 0; i < 32; i++ {
		core.WriteByte(0, b)
	}
	core.WriteUInt32LE(f.FileAttributes, b)
	for i := 0; i < 16; i++ {
		core.WriteByte(0, b)
	}
	core.WriteBytes(f.LastWriteTime[:], b)
	core.WriteUInt32LE(f.FileSizeHigh, b)
	core.WriteUInt32LE(f.FileSizeLow, b)
	name := make([]byte, 512)
	copy(name, f.FileName)
	core.WriteBytes(name, b)
	return b.Bytes()
}

func (f *FileDescriptor) isDir() bool {
	if f.Flags&FD_ATTRIBUTES != 0 {
		return f.FileAttributes&FILE_ATTRIBUTE_DIRECTORY != 0
	}
	return false
}

func (f *FileDescriptor) hasFileSize() bool {
	return f.Flags&FD_FILESIZE != 0
}

// temp dir
type CliprdrTempDirectory struct {
	SzTempDir []byte `struc:"[260]byte"`
}

// format list
type CliprdrFormat struct {
	FormatId   uint32
	FormatName string
}
type CliprdrFormatList struct {
	NumFormats uint32
	Formats    []CliprdrFormat
}
type ClipboardFormats uint16

const (
	CB_FORMAT_HTML             = 0xD010
	CB_FORMAT_PNG              = 0xD011
	CB_FORMAT_JPEG             = 0xD012
	CB_FORMAT_GIF              = 0xD013
	CB_FORMAT_TEXTURILIST      = 0xD014
	CB_FORMAT_GNOMECOPIEDFILES = 0xD015
	CB_FORMAT_MATECOPIEDFILES  = 0xD016
)

// Standard clipboard format IDs
const (
	CF_TEXT        = 1
	CF_UNICODETEXT = 13
)

// lock or unlock
type CliprdrCtrlClipboardData struct {
	ClipDataId uint32
}

// format data
type CliprdrFormatDataRequest struct {
	RequestedFormatId uint32
}
type CliprdrFormatDataResponse struct {
	RequestedFormatData []byte
}

// file contents
type CliprdrFileContentsRequest struct {
	StreamId      uint32 `struc:"little"`
	Lindex        uint32 `struc:"little"`
	DwFlags       uint32 `struc:"little"`
	NPositionLow  uint32 `struc:"little"`
	NPositionHigh uint32 `struc:"little"`
	CbRequested   uint32 `struc:"little"`
	ClipDataId    uint32 `struc:"little"`
}

func FileContentsSizeRequest(i uint32) *CliprdrFileContentsRequest {
	return &CliprdrFileContentsRequest{
		StreamId:      1,
		Lindex:        i,
		DwFlags:       FILECONTENTS_SIZE,
		NPositionLow:  0,
		NPositionHigh: 0,
		CbRequested:   65535,
		ClipDataId:    0,
	}
}

type CliprdrFileContentsResponse struct {
	StreamId      uint32
	CbRequested   uint32
	RequestedData []byte
}

func (resp *CliprdrFileContentsResponse) Unpack(b []byte) {
	r := bytes.NewReader(b)
	resp.StreamId, _ = core.ReadUInt32LE(r)
	resp.CbRequested = uint32(r.Len())
	resp.RequestedData, _ = core.ReadBytes(int(resp.CbRequested), r)
}

// sendClipPDU sends a CLIPRDR PDU with the standard 8-byte header
// (msgType + msgFlags + dataLen) prepended to body.
func sendClipPDU(sender core.ChannelSender, msgType, msgFlags uint16, body []byte) {
	b := &bytes.Buffer{}
	core.WriteUInt16LE(msgType, b)
	core.WriteUInt16LE(msgFlags, b)
	core.WriteUInt32LE(uint32(len(body)), b)
	b.Write(body)
	sender.SendToChannel(ChannelName, b.Bytes())
}
