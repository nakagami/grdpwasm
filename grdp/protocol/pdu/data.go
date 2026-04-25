package pdu

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/lunixbochs/struc"
	"github.com/nakagami/grdp/core"
)

// DecodeRemoteFX is a pluggable decoder for RemoteFX (MS-RDPRFX) surface codec
// data. It is set at init time by the main client package to avoid a circular
// import between protocol/pdu and plugin/rdpgfx.
// The function receives raw RFX data and returns top-down BGRA pixels.
var DecodeRemoteFX func(data []byte, width, height int) []byte

const (
	PDUTYPE_DEMANDACTIVEPDU  = 0x11
	PDUTYPE_CONFIRMACTIVEPDU = 0x13
	PDUTYPE_DEACTIVATEALLPDU = 0x16
	PDUTYPE_DATAPDU          = 0x17
	PDUTYPE_SERVER_REDIR_PKT = 0x1A
)

type PduType2 uint8

const (
	PDUTYPE2_UPDATE                      = 0x02
	PDUTYPE2_CONTROL                     = 0x14
	PDUTYPE2_POINTER                     = 0x1B
	PDUTYPE2_INPUT                       = 0x1C
	PDUTYPE2_SYNCHRONIZE                 = 0x1F
	PDUTYPE2_REFRESH_RECT                = 0x21
	PDUTYPE2_PLAY_SOUND                  = 0x22
	PDUTYPE2_SUPPRESS_OUTPUT             = 0x23
	PDUTYPE2_SHUTDOWN_REQUEST            = 0x24
	PDUTYPE2_SHUTDOWN_DENIED             = 0x25
	PDUTYPE2_SAVE_SESSION_INFO           = 0x26
	PDUTYPE2_FONTLIST                    = 0x27
	PDUTYPE2_FONTMAP                     = 0x28
	PDUTYPE2_SET_KEYBOARD_INDICATORS     = 0x29
	PDUTYPE2_BITMAPCACHE_PERSISTENT_LIST = 0x2B
	PDUTYPE2_BITMAPCACHE_ERROR_PDU       = 0x2C
	PDUTYPE2_SET_KEYBOARD_IME_STATUS     = 0x2D
	PDUTYPE2_OFFSCRCACHE_ERROR_PDU       = 0x2E
	PDUTYPE2_SET_ERROR_INFO_PDU          = 0x2F
	PDUTYPE2_DRAWNINEGRID_ERROR_PDU      = 0x30
	PDUTYPE2_DRAWGDIPLUS_ERROR_PDU       = 0x31
	PDUTYPE2_ARC_STATUS_PDU              = 0x32
	PDUTYPE2_STATUS_INFO_PDU             = 0x36
	PDUTYPE2_MONITOR_LAYOUT_PDU          = 0x37
	PDUTYPE2_FRAME_ACKNOWLEDGE           = 0x38
)

// Slow-Path Pointer Update types (MS-RDPBCGR 2.2.9.1.1.4)
const (
	TS_PTRUPDATE_TYPE_SYSTEM   = 0x0001
	TS_PTRUPDATE_TYPE_POSITION = 0x0003
	TS_PTRUPDATE_TYPE_COLOR    = 0x0006
	TS_PTRUPDATE_TYPE_CACHED   = 0x0007
	TS_PTRUPDATE_TYPE_POINTER  = 0x0008
)

func (p PduType2) String() string {
	switch p {
	case PDUTYPE2_UPDATE:
		return "PDUTYPE2_UPDATE"
	case PDUTYPE2_CONTROL:
		return "PDUTYPE2_CONTROL"
	case PDUTYPE2_POINTER:
		return "PDUTYPE2_POINTER"
	case PDUTYPE2_INPUT:
		return "PDUTYPE2_INPUT"
	case PDUTYPE2_SYNCHRONIZE:
		return "PDUTYPE2_SYNCHRONIZE"
	case PDUTYPE2_REFRESH_RECT:
		return "PDUTYPE2_REFRESH_RECT"
	case PDUTYPE2_PLAY_SOUND:
		return "PDUTYPE2_PLAY_SOUND"
	case PDUTYPE2_SUPPRESS_OUTPUT:
		return "PDUTYPE2_SUPPRESS_OUTPUT"
	case PDUTYPE2_SHUTDOWN_REQUEST:
		return "PDUTYPE2_SHUTDOWN_REQUEST"
	case PDUTYPE2_SHUTDOWN_DENIED:
		return "PDUTYPE2_SHUTDOWN_DENIED"
	case PDUTYPE2_SAVE_SESSION_INFO:
		return "PDUTYPE2_SAVE_SESSION_INFO"
	case PDUTYPE2_FONTLIST:
		return "PDUTYPE2_FONTLIST"
	case PDUTYPE2_FONTMAP:
		return "PDUTYPE2_FONTMAP"
	case PDUTYPE2_SET_KEYBOARD_INDICATORS:
		return "PDUTYPE2_SET_KEYBOARD_INDICATORS"
	case PDUTYPE2_BITMAPCACHE_PERSISTENT_LIST:
		return "PDUTYPE2_BITMAPCACHE_PERSISTENT_LIST"
	case PDUTYPE2_BITMAPCACHE_ERROR_PDU:
		return "PDUTYPE2_BITMAPCACHE_ERROR_PDU"
	case PDUTYPE2_SET_KEYBOARD_IME_STATUS:
		return "PDUTYPE2_SET_KEYBOARD_IME_STATUS"
	case PDUTYPE2_OFFSCRCACHE_ERROR_PDU:
		return "PDUTYPE2_OFFSCRCACHE_ERROR_PDU"
	case PDUTYPE2_SET_ERROR_INFO_PDU:
		return "PDUTYPE2_SET_ERROR_INFO_PDU"
	case PDUTYPE2_DRAWNINEGRID_ERROR_PDU:
		return "PDUTYPE2_DRAWNINEGRID_ERROR_PDU"
	case PDUTYPE2_DRAWGDIPLUS_ERROR_PDU:
		return "PDUTYPE2_DRAWGDIPLUS_ERROR_PDU"
	case PDUTYPE2_ARC_STATUS_PDU:
		return "PDUTYPE2_ARC_STATUS_PDU"
	case PDUTYPE2_STATUS_INFO_PDU:
		return "PDUTYPE2_STATUS_INFO_PDU"
	case PDUTYPE2_MONITOR_LAYOUT_PDU:
		return "PDUTYPE2_MONITOR_LAYOUT_PDU"
	}

	return "Unknown"
}

const (
	CTRLACTION_REQUEST_CONTROL = 0x0001
	CTRLACTION_GRANTED_CONTROL = 0x0002
	CTRLACTION_DETACH          = 0x0003
	CTRLACTION_COOPERATE       = 0x0004
)

const (
	STREAM_UNDEFINED = 0x00
	STREAM_LOW       = 0x01
	STREAM_MED       = 0x02
	STREAM_HI        = 0x04
)

type FastPathUpdateType uint8

const (
	FASTPATH_UPDATETYPE_ORDERS        = 0x0
	FASTPATH_UPDATETYPE_BITMAP        = 0x1
	FASTPATH_UPDATETYPE_PALETTE       = 0x2
	FASTPATH_UPDATETYPE_SYNCHRONIZE   = 0x3
	FASTPATH_UPDATETYPE_SURFCMDS      = 0x4
	FASTPATH_UPDATETYPE_PTR_NULL      = 0x5
	FASTPATH_UPDATETYPE_PTR_DEFAULT   = 0x6
	FASTPATH_UPDATETYPE_PTR_POSITION  = 0x8
	FASTPATH_UPDATETYPE_COLOR         = 0x9
	FASTPATH_UPDATETYPE_CACHED        = 0xA
	FASTPATH_UPDATETYPE_POINTER       = 0xB
	FASTPATH_UPDATETYPE_LARGE_POINTER = 0xC
)

func (t FastPathUpdateType) String() string {
	switch t {
	case FASTPATH_UPDATETYPE_ORDERS:
		return "FASTPATH_UPDATETYPE_ORDERS"
	case FASTPATH_UPDATETYPE_BITMAP:
		return "FASTPATH_UPDATETYPE_BITMAP"
	case FASTPATH_UPDATETYPE_PALETTE:
		return "FASTPATH_UPDATETYPE_PALETTE"
	case FASTPATH_UPDATETYPE_SYNCHRONIZE:
		return "FASTPATH_UPDATETYPE_SYNCHRONIZE"
	case FASTPATH_UPDATETYPE_SURFCMDS:
		return "FASTPATH_UPDATETYPE_SURFCMDS"
	case FASTPATH_UPDATETYPE_PTR_NULL:
		return "FASTPATH_UPDATETYPE_PTR_NULL"
	case FASTPATH_UPDATETYPE_PTR_DEFAULT:
		return "FASTPATH_UPDATETYPE_PTR_DEFAULT"
	case FASTPATH_UPDATETYPE_PTR_POSITION:
		return "FASTPATH_UPDATETYPE_PTR_POSITION"
	case FASTPATH_UPDATETYPE_COLOR:
		return "FASTPATH_UPDATETYPE_COLOR"
	case FASTPATH_UPDATETYPE_CACHED:
		return "FASTPATH_UPDATETYPE_CACHED"
	case FASTPATH_UPDATETYPE_POINTER:
		return "FASTPATH_UPDATETYPE_POINTER"
	case FASTPATH_UPDATETYPE_LARGE_POINTER:
		return "FASTPATH_UPDATETYPE_LARGE_POINTER"
	}

	return "Unknown"
}

const (
	BITMAP_COMPRESSION = 0x0001
	//NO_BITMAP_COMPRESSION_HDR = 0x0400
	BITMAP_NO_PROCESSING = 0x8000 // Surface command: data is already decoded top-down BGRA
)

// Surface Command types (MS-RDPBCGR 2.2.9.1.2.1)
const (
	CMDTYPE_SET_SURFACE_BITS    = 0x0001
	CMDTYPE_FRAME_MARKER        = 0x0004
	CMDTYPE_STREAM_SURFACE_BITS = 0x0006
)

const (
	SURFCMD_FRAMEACTION_BEGIN = 0x0000
	SURFCMD_FRAMEACTION_END   = 0x0001
)

/* compression types */
const (
	RDP_MPPC_BIG        = 0x01
	RDP_MPPC_COMPRESSED = 0x20
	RDP_MPPC_RESET      = 0x40
	RDP_MPPC_FLUSH      = 0x80
	RDP_MPPC_DICT_SIZE  = 65536
)

type ShareDataHeader struct {
	SharedId           uint32 `struc:"little"`
	Padding1           uint8  `struc:"little"`
	StreamId           uint8  `struc:"little"`
	UncompressedLength uint16 `struc:"little"`
	PDUType2           uint8  `struc:"little"`
	CompressedType     uint8  `struc:"little"`
	CompressedLength   uint16 `struc:"little"`
}

func NewShareDataHeader(size int, type2 uint8, shareId uint32) *ShareDataHeader {
	return &ShareDataHeader{
		SharedId:           shareId,
		PDUType2:           type2,
		StreamId:           STREAM_LOW,
		UncompressedLength: uint16(size + 4),
	}
}

type PDUMessage interface {
	Type() uint16
	Serialize() []byte
}

type DemandActivePDU struct {
	SharedId                   uint32       `struc:"little"`
	LengthSourceDescriptor     uint16       `struc:"little,sizeof=SourceDescriptor"`
	LengthCombinedCapabilities uint16       `struc:"little"`
	SourceDescriptor           []byte       `struc:"sizefrom=LengthSourceDescriptor"`
	NumberCapabilities         uint16       `struc:"little,sizeof=CapabilitySets"`
	Pad2Octets                 uint16       `struc:"little"`
	CapabilitySets             []Capability `struc:"sizefrom=NumberCapabilities"`
	SessionId                  uint32       `struc:"little"`
}

func (d *DemandActivePDU) Type() uint16 {
	return PDUTYPE_DEMANDACTIVEPDU
}

func (d *DemandActivePDU) Serialize() []byte {
	buff := &bytes.Buffer{}
	core.WriteUInt32LE(d.SharedId, buff)
	core.WriteUInt16LE(d.LengthSourceDescriptor, buff)
	core.WriteUInt16LE(d.LengthCombinedCapabilities, buff)
	core.WriteBytes([]byte(d.SourceDescriptor), buff)
	core.WriteUInt16LE(uint16(len(d.CapabilitySets)), buff)
	core.WriteUInt16LE(d.Pad2Octets, buff)
	for _, cap := range d.CapabilitySets {
		core.WriteUInt16LE(uint16(cap.Type()), buff)
		capBuff := &bytes.Buffer{}
		struc.Pack(capBuff, cap)
		capBytes := capBuff.Bytes()
		core.WriteUInt16LE(uint16(len(capBytes)+4), buff)
		core.WriteBytes(capBytes, buff)
	}
	core.WriteUInt32LE(d.SessionId, buff)
	return buff.Bytes()
}

func readDemandActivePDU(r io.Reader) (*DemandActivePDU, error) {
	d := &DemandActivePDU{}
	var err error
	d.SharedId, err = core.ReadUInt32LE(r)
	if err != nil {
		return nil, err
	}
	d.LengthSourceDescriptor, err = core.ReadUint16LE(r)
	d.LengthCombinedCapabilities, err = core.ReadUint16LE(r)
	sourceDescriptorBytes, err := core.ReadBytes(int(d.LengthSourceDescriptor), r)
	if err != nil {
		return nil, err
	}
	d.SourceDescriptor = sourceDescriptorBytes
	d.NumberCapabilities, err = core.ReadUint16LE(r)
	d.Pad2Octets, err = core.ReadUint16LE(r)
	d.CapabilitySets = make([]Capability, 0, d.NumberCapabilities)
	for i := 0; i < int(d.NumberCapabilities); i++ {
		c, err := readCapability(r)
		if err != nil {
			//return nil, err
			continue
		}
		d.CapabilitySets = append(d.CapabilitySets, c)
	}
	d.NumberCapabilities = uint16(len(d.CapabilitySets))
	d.SessionId, err = core.ReadUInt32LE(r)
	if err != nil {
		return nil, err
	}
	return d, nil
}

type ConfirmActivePDU struct {
	SharedId                   uint32       `struc:"little"`
	OriginatorId               uint16       `struc:"little"`
	LengthSourceDescriptor     uint16       `struc:"little,sizeof=SourceDescriptor"`
	LengthCombinedCapabilities uint16       `struc:"little"`
	SourceDescriptor           []byte       `struc:"sizefrom=LengthSourceDescriptor"`
	NumberCapabilities         uint16       `struc:"little,sizeof=CapabilitySets"`
	Pad2Octets                 uint16       `struc:"little"`
	CapabilitySets             []Capability `struc:"sizefrom=NumberCapabilities"`
}

func (*ConfirmActivePDU) Type() uint16 {
	return PDUTYPE_CONFIRMACTIVEPDU
}

func (c *ConfirmActivePDU) Serialize() []byte {
	buff := &bytes.Buffer{}
	core.WriteUInt32LE(c.SharedId, buff)
	core.WriteUInt16LE(c.OriginatorId, buff)
	core.WriteUInt16LE(uint16(len(c.SourceDescriptor)), buff)

	capsBuff := &bytes.Buffer{}
	for _, capa := range c.CapabilitySets {
		core.WriteUInt16LE(uint16(capa.Type()), capsBuff)
		capBuff := &bytes.Buffer{}
		struc.Pack(capBuff, capa)
		capBytes := capBuff.Bytes()
		core.WriteUInt16LE(uint16(len(capBytes)+4), capsBuff)
		core.WriteBytes(capBytes, capsBuff)
	}
	capsBytes := capsBuff.Bytes()

	core.WriteUInt16LE(uint16(2+2+len(capsBytes)), buff)
	core.WriteBytes(c.SourceDescriptor, buff)
	core.WriteUInt16LE(c.NumberCapabilities, buff)
	core.WriteUInt16LE(c.Pad2Octets, buff)
	core.WriteBytes(capsBytes, buff)
	return buff.Bytes()
}

// 9401 => share control header
// 1300 => share control header
// ec03 => share control header
// ea030100  => shareId 66538
// ea03 => OriginatorId
// 0400
// 8001 => LengthCombinedCapabilities
// 72647079
// 0c00 => NumberCapabilities 12
// 0000
// caps below
// 010018000100030000020000000015040000000000000000
// 02001c00180001000100010000052003000000000100000001000000
// 030058000000000000000000000000000000000000000000010014000000010000000a0000000000000000000000000000000000000000000000000000000000000000000000000000000000008403000000000000000000
// 04002800000000000000000000000000000000000000000000000000000000000000000000000000
// 0800080000001400
// 0c00080000000000
// 0d005c001500000009040000040000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000c000000
// 0f00080000000000
// 10003400000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000
// 11000c000000000000000000
// 14000c000000000000000000
// 1a00080000000000

func NewConfirmActivePDU() *ConfirmActivePDU {
	return &ConfirmActivePDU{
		OriginatorId:     0x03EA,
		CapabilitySets:   make([]Capability, 0),
		SourceDescriptor: []byte("rdpy"),
	}
}

func readConfirmActivePDU(r io.Reader) (*ConfirmActivePDU, error) {
	p := &ConfirmActivePDU{}
	var err error
	p.SharedId, err = core.ReadUInt32LE(r)
	if err != nil {
		return nil, err
	}
	p.OriginatorId, err = core.ReadUint16LE(r)
	p.LengthSourceDescriptor, err = core.ReadUint16LE(r)
	p.LengthCombinedCapabilities, err = core.ReadUint16LE(r)

	sourceDescriptorBytes, err := core.ReadBytes(int(p.LengthSourceDescriptor), r)
	if err != nil {
		return nil, err
	}
	p.SourceDescriptor = sourceDescriptorBytes
	p.NumberCapabilities, err = core.ReadUint16LE(r)
	p.Pad2Octets, err = core.ReadUint16LE(r)

	p.CapabilitySets = make([]Capability, 0)
	for i := 0; i < int(p.NumberCapabilities); i++ {
		c, err := readCapability(r)
		if err != nil {
			return nil, err
		}
		p.CapabilitySets = append(p.CapabilitySets, c)
	}
	s, _ := core.ReadUInt32LE(r)
	slog.Debug("readConfirmActivePDU", "sessionid", s)
	return p, nil
}

type DeactiveAllPDU struct {
	ShareId                uint32 `struc:"little"`
	LengthSourceDescriptor uint16 `struc:"little,sizeof=SourceDescriptor"`
	SourceDescriptor       []byte
}

func (*DeactiveAllPDU) Type() uint16 {
	return PDUTYPE_DEACTIVATEALLPDU
}

func (d *DeactiveAllPDU) Serialize() []byte {
	buff := &bytes.Buffer{}
	struc.Pack(buff, d)
	return buff.Bytes()
}

func readDeactiveAllPDU(r io.Reader) (*DeactiveAllPDU, error) {
	p := &DeactiveAllPDU{}
	err := struc.Unpack(r, p)
	return p, err
}

// ServerRedirectionPDU represents the RDP Server Redirection PDU
// (MS-RDPBCGR 2.2.13.2.1). Only the LoadBalanceInfo field (routing
// token) is extracted; other optional fields are skipped.
type ServerRedirectionPDU struct {
	Flags           uint16
	Length          uint16
	SessionID       uint32
	RedirFlags      uint32
	LoadBalanceInfo []byte
}

const (
	LB_TARGET_NET_ADDRESS = 0x00000001
	LB_LOAD_BALANCE_INFO  = 0x00000002
	LB_USERNAME           = 0x00000004
)

func (*ServerRedirectionPDU) Type() uint16 {
	return PDUTYPE_SERVER_REDIR_PKT
}

func (d *ServerRedirectionPDU) Serialize() []byte {
	return nil
}

func readServerRedirectionPDU(r io.Reader) (*ServerRedirectionPDU, error) {
	// Enhanced Security variant has a 2-byte pad before the PDU body
	if _, err := core.ReadUint16LE(r); err != nil {
		return nil, fmt.Errorf("redir: read pad: %w", err)
	}

	redir := &ServerRedirectionPDU{}
	var err error
	if redir.Flags, err = core.ReadUint16LE(r); err != nil {
		return nil, fmt.Errorf("redir: read flags: %w", err)
	}
	if redir.Length, err = core.ReadUint16LE(r); err != nil {
		return nil, fmt.Errorf("redir: read length: %w", err)
	}
	if redir.SessionID, err = core.ReadUInt32LE(r); err != nil {
		return nil, fmt.Errorf("redir: read sessionID: %w", err)
	}
	if redir.RedirFlags, err = core.ReadUInt32LE(r); err != nil {
		return nil, fmt.Errorf("redir: read redirFlags: %w", err)
	}

	// Parse variable-length fields in flag order.
	// We only need LoadBalanceInfo (routing token) for reconnection.
	if redir.RedirFlags&LB_TARGET_NET_ADDRESS != 0 {
		cbLen, err := core.ReadUInt32LE(r)
		if err != nil {
			return nil, fmt.Errorf("redir: read targetNetAddr len: %w", err)
		}
		if _, err := core.ReadBytes(int(cbLen), r); err != nil {
			return nil, fmt.Errorf("redir: read targetNetAddr: %w", err)
		}
	}

	if redir.RedirFlags&LB_LOAD_BALANCE_INFO != 0 {
		cbLen, err := core.ReadUInt32LE(r)
		if err != nil {
			return nil, fmt.Errorf("redir: read loadBalanceInfo len: %w", err)
		}
		redir.LoadBalanceInfo, err = core.ReadBytes(int(cbLen), r)
		if err != nil {
			return nil, fmt.Errorf("redir: read loadBalanceInfo: %w", err)
		}
	}

	slog.Debug("Server Redirection PDU",
		"flags", fmt.Sprintf("0x%04x", redir.Flags),
		"sessionID", redir.SessionID,
		"redirFlags", fmt.Sprintf("0x%08x", redir.RedirFlags),
		"loadBalanceInfo", string(redir.LoadBalanceInfo))
	return redir, nil
}

type DataPDU struct {
	Header *ShareDataHeader
	Data   DataPDUData
}

func (*DataPDU) Type() uint16 {
	return PDUTYPE_DATAPDU
}

func (d *DataPDU) Serialize() []byte {
	buff := &bytes.Buffer{}
	struc.Pack(buff, d.Header)
	struc.Pack(buff, d.Data)
	return buff.Bytes()
}

func NewDataPDU(data DataPDUData, shareId uint32) *DataPDU {
	dataBuff := &bytes.Buffer{}
	struc.Pack(dataBuff, data)
	return &DataPDU{
		Header: NewShareDataHeader(len(dataBuff.Bytes()), data.Type2(), shareId),
		Data:   data,
	}
}

func readDataPDU(r io.Reader) (*DataPDU, error) {
	header := &ShareDataHeader{}
	err := struc.Unpack(r, header)
	if err != nil {
		slog.Error("readDataPDU", "err", err)
		return nil, err
	}
	var d DataPDUData
	slog.Debug("readDataPDU", "PDUTYPE2", header.PDUType2)
	switch header.PDUType2 {
	case PDUTYPE2_UPDATE:
		d = &UpdateDataPDU{}

	case PDUTYPE2_SYNCHRONIZE:
		d = &SynchronizeDataPDU{}

	case PDUTYPE2_CONTROL:
		d = &ControlDataPDU{}

	case PDUTYPE2_FONTLIST:
		d = &FontListDataPDU{}

	case PDUTYPE2_SET_ERROR_INFO_PDU:
		d = &ErrorInfoDataPDU{}

	case PDUTYPE2_FONTMAP:
		d = &FontMapDataPDU{}

	case PDUTYPE2_SAVE_SESSION_INFO:
		d = &SaveSessionInfo{}

	case PDUTYPE2_POINTER:
		d = &PointerDataPDU{}

	default:
		err = errors.New(fmt.Sprintf("Unknown data pdu type2 0x%02x", header.PDUType2))
		slog.Error("readDataPDU", "err", err)
		return nil, err
	}

	err = d.Unpack(r)
	if err != nil {
		slog.Error("readDataPDU", "err", err)
		return nil, err
	}

	p := &DataPDU{
		Header: header,
		Data:   d,
	}
	return p, nil
}

type DataPDUData interface {
	Type2() uint8
	Unpack(io.Reader) error
}

type UpdateDataPDU struct {
	UpdateType uint16
	Udata      UpdateData
}

func (*UpdateDataPDU) Type2() uint8 {
	return PDUTYPE2_UPDATE
}
func (d *UpdateDataPDU) Unpack(r io.Reader) (err error) {
	//slow path update
	d.UpdateType, err = core.ReadUint16LE(r)
	slog.Debug("FastPathUpdate", "type", d.UpdateType)
	var p UpdateData
	switch d.UpdateType {
	case FASTPATH_UPDATETYPE_ORDERS:
	case FASTPATH_UPDATETYPE_BITMAP:
		p = &BitmapUpdateDataPDU{}
	case FASTPATH_UPDATETYPE_PALETTE:
	case FASTPATH_UPDATETYPE_SYNCHRONIZE:
	}
	if p != nil {
		err = p.Unpack(r)
		if err != nil {
			//slog.Error("Unpack:", err)
			return err
		}
	} else {
		return errors.New(fmt.Sprintf("Unsupport slow update type 0x%x", d.UpdateType))
	}

	d.Udata = p

	return nil
}

// PointerDataPDU handles slow-path pointer updates (MS-RDPBCGR 2.2.9.1.1.4)
type PointerDataPDU struct {
	MessageType uint16
	Pad2Octets  uint16
	Pdata       UpdateData
}

func (*PointerDataPDU) Type2() uint8 {
	return PDUTYPE2_POINTER
}

func (d *PointerDataPDU) Unpack(r io.Reader) error {
	var err error
	d.MessageType, err = core.ReadUint16LE(r)
	if err != nil {
		return err
	}
	d.Pad2Octets, err = core.ReadUint16LE(r)
	if err != nil {
		return err
	}
	slog.Debug("PointerDataPDU", "messageType", d.MessageType)
	var p UpdateData
	switch d.MessageType {
	case TS_PTRUPDATE_TYPE_CACHED:
		p = &FastPathUpdateCachedPDU{}
	case TS_PTRUPDATE_TYPE_POINTER:
		p = &FastPathUpdatePointerPDU{}
	case TS_PTRUPDATE_TYPE_SYSTEM, TS_PTRUPDATE_TYPE_POSITION, TS_PTRUPDATE_TYPE_COLOR:
		// not yet parsed; remaining data is discarded by the caller
	default:
		slog.Debug("PointerDataPDU: unhandled", "messageType", d.MessageType)
	}
	if p != nil {
		if err = p.Unpack(r); err != nil {
			return err
		}
	}
	d.Pdata = p
	return nil
}

func (d *PointerDataPDU) Serialize() []byte {
	return nil
}

type BitmapUpdateDataPDU struct {
	NumberRectangles uint16 `struc:"little,sizeof=Rectangles"`
	Rectangles       []BitmapData
}

func (*BitmapUpdateDataPDU) FastPathUpdateType() uint8 {
	return FASTPATH_UPDATETYPE_BITMAP
}
func (f *BitmapUpdateDataPDU) Unpack(r io.Reader) error {
	var err error
	f.NumberRectangles, err = core.ReadUint16LE(r)
	f.Rectangles = make([]BitmapData, 0, f.NumberRectangles)
	for i := 0; i < int(f.NumberRectangles); i++ {
		rect := BitmapData{}
		rect.DestLeft, err = core.ReadUint16LE(r)
		rect.DestTop, err = core.ReadUint16LE(r)
		rect.DestRight, err = core.ReadUint16LE(r)
		rect.DestBottom, err = core.ReadUint16LE(r)
		rect.Width, err = core.ReadUint16LE(r)
		rect.Height, err = core.ReadUint16LE(r)
		rect.BitsPerPixel, err = core.ReadUint16LE(r)
		rect.Flags, err = core.ReadUint16LE(r)
		rect.BitmapLength, err = core.ReadUint16LE(r)
		ln := rect.BitmapLength
		if rect.Flags&BITMAP_COMPRESSION != 0 && (rect.Flags&NO_BITMAP_COMPRESSION_HDR == 0) {
			rect.BitmapComprHdr = new(BitmapCompressedDataHeader)
			rect.BitmapComprHdr.CbCompFirstRowSize, err = core.ReadUint16LE(r)
			rect.BitmapComprHdr.CbCompMainBodySize, err = core.ReadUint16LE(r)
			rect.BitmapComprHdr.CbScanWidth, err = core.ReadUint16LE(r)
			rect.BitmapComprHdr.CbUncompressedSize, err = core.ReadUint16LE(r)
			ln = rect.BitmapComprHdr.CbCompMainBodySize
		}

		rect.BitmapDataStream, err = core.ReadBytes(int(ln), r)
		f.Rectangles = append(f.Rectangles, rect)
	}
	return err
}

type SynchronizeDataPDU struct {
	MessageType uint16 `struc:"little"`
	TargetUser  uint16 `struc:"little"`
}

func (*SynchronizeDataPDU) Type2() uint8 {
	return PDUTYPE2_SYNCHRONIZE
}

func NewSynchronizeDataPDU(targetUser uint16) *SynchronizeDataPDU {
	return &SynchronizeDataPDU{
		MessageType: 1,
		TargetUser:  targetUser,
	}
}
func (d *SynchronizeDataPDU) Unpack(r io.Reader) error {
	return struc.Unpack(r, d)
}

type ControlDataPDU struct {
	Action    uint16 `struc:"little"`
	GrantId   uint16 `struc:"little"`
	ControlId uint32 `struc:"little"`
}

func (*ControlDataPDU) Type2() uint8 {
	return PDUTYPE2_CONTROL
}
func (d *ControlDataPDU) Unpack(r io.Reader) error {
	return struc.Unpack(r, d)
}

type FontListDataPDU struct {
	NumberFonts   uint16 `struc:"little"`
	TotalNumFonts uint16 `struc:"little"`
	ListFlags     uint16 `struc:"little"`
	EntrySize     uint16 `struc:"little"`
}

func (*FontListDataPDU) Type2() uint8 {
	return PDUTYPE2_FONTLIST
}
func (d *FontListDataPDU) Unpack(r io.Reader) error {
	return struc.Unpack(r, d)
}

type ErrorInfoDataPDU struct {
	ErrorInfo uint32 `struc:"little"`
}

func (*ErrorInfoDataPDU) Type2() uint8 {
	return PDUTYPE2_SET_ERROR_INFO_PDU
}
func (d *ErrorInfoDataPDU) Unpack(r io.Reader) error {
	return struc.Unpack(r, d)
}

type FontMapDataPDU struct {
	NumberEntries   uint16 `struc:"little"`
	TotalNumEntries uint16 `struc:"little"`
	MapFlags        uint16 `struc:"little"`
	EntrySize       uint16 `struc:"little"`
}

func (*FontMapDataPDU) Type2() uint8 {
	return PDUTYPE2_FONTMAP
}
func (d *FontMapDataPDU) Unpack(r io.Reader) error {
	err := struc.Unpack(r, d)
	// MS-RDPBCGR 2.2.1.22.1: Font Map payload fields are optional.
	// VirtualBox sends a short FontMap PDU with no payload data.
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return nil
	}
	return err
}

// SuppressOutputPDU tells the server to start/stop sending display updates.
// MS-RDPBCGR 2.2.11.3.1
type SuppressOutputPDU struct {
	AllowDisplayUpdates uint8   `struc:"little"`
	Pad3Octets          [3]byte `struc:"little"`
	Left                uint16  `struc:"little"`
	Top                 uint16  `struc:"little"`
	Right               uint16  `struc:"little"`
	Bottom              uint16  `struc:"little"`
}

func (*SuppressOutputPDU) Type2() uint8 {
	return PDUTYPE2_SUPPRESS_OUTPUT
}
func (d *SuppressOutputPDU) Unpack(r io.Reader) error {
	return struc.Unpack(r, d)
}

// FrameAcknowledgeDataPDU acknowledges receipt of a frame (MS-RDPBCGR 2.2.11.3.2).
type FrameAcknowledgeDataPDU struct {
	FrameID uint32 `struc:"little"`
}

func (*FrameAcknowledgeDataPDU) Type2() uint8 {
	return PDUTYPE2_FRAME_ACKNOWLEDGE
}
func (d *FrameAcknowledgeDataPDU) Unpack(r io.Reader) error {
	return struc.Unpack(r, d)
}

// RefreshRectPDU requests the server to redraw one or more screen regions.
// MS-RDPBCGR 2.2.11.2
type RefreshRectPDU struct {
	NumberOfAreas uint8   `struc:"little"`
	Pad3Octets    [3]byte `struc:"little"`
	Left          uint16  `struc:"little"`
	Top           uint16  `struc:"little"`
	Right         uint16  `struc:"little"`
	Bottom        uint16  `struc:"little"`
}

func (*RefreshRectPDU) Type2() uint8 {
	return PDUTYPE2_REFRESH_RECT
}
func (d *RefreshRectPDU) Unpack(r io.Reader) error {
	return struc.Unpack(r, d)
}

type InfoType uint32

const (
	INFOTYPE_LOGON               = 0x00000000
	INFOTYPE_LOGON_LONG          = 0x00000001
	INFOTYPE_LOGON_PLAINNOTIFY   = 0x00000002
	INFOTYPE_LOGON_EXTENDED_INFO = 0x00000003
)
const (
	LOGON_EX_AUTORECONNECTCOOKIE = 0x00000001
	LOGON_EX_LOGONERRORS         = 0x00000002
)

type LogonFields struct {
	CbFileData uint32   `struc:"little"`
	Len        uint32   //28 `struc:"little"`
	Version    uint32   // 1 `struc:"little"`
	LogonId    uint32   `struc:"little"`
	random     [16]byte //16 `struc:"little"`
}
type SaveSessionInfo struct {
	InfoType      uint32
	Length        uint16
	FieldsPresent uint32
	LogonId       uint32
	Random        []byte
}

func (s *SaveSessionInfo) logonInfoV1(r io.Reader) (err error) {
	core.ReadUInt32LE(r) // cbDomain
	b, _ := core.ReadBytes(52, r)
	domain := core.UnicodeDecode(b)

	core.ReadUInt32LE(r) // cbUserName
	b, _ = core.ReadBytes(512, r)
	userName := core.UnicodeDecode(b)

	sessionId, _ := core.ReadUInt32LE(r)
	s.LogonId = sessionId
	slog.Debug(fmt.Sprintf("SessionId:[%d] UserName:[%s] Domain:[%s]", s.LogonId, userName, domain))
	return err
}
func (s *SaveSessionInfo) logonInfoV2(r io.Reader) (err error) {
	core.ReadUint16LE(r)
	core.ReadUInt32LE(r)
	sessionId, _ := core.ReadUInt32LE(r)
	s.LogonId = sessionId
	cbDomain, _ := core.ReadUInt32LE(r)
	cbUserName, _ := core.ReadUInt32LE(r)
	core.ReadBytes(558, r)

	b, _ := core.ReadBytes(int(cbDomain), r)
	domain := core.UnicodeDecode(b)
	b, _ = core.ReadBytes(int(cbUserName), r)
	userName := core.UnicodeDecode(b)
	slog.Debug(fmt.Sprintf("SessionId:[%d] UserName:[ %s] Domain:[ %s]", s.LogonId, userName, domain))

	return err
}
func (s *SaveSessionInfo) logonPlainNotify(r io.Reader) (err error) {
	core.ReadBytes(576, r) /* pad (576 bytes) */
	return err
}
func (s *SaveSessionInfo) logonInfoExtended(r io.Reader) (err error) {
	s.Length, err = core.ReadUint16LE(r)
	s.FieldsPresent, err = core.ReadUInt32LE(r)
	//slog.Debug("FieldsPresent:", s.FieldsPresent)
	// auto reconnect cookie
	if s.FieldsPresent&LOGON_EX_AUTORECONNECTCOOKIE != 0 {
		core.ReadUInt32LE(r)
		b, _ := core.ReadUInt32LE(r)
		if b != 28 {
			return errors.New(fmt.Sprintf("invalid length in Auto-Reconnect packet"))
		}
		b, _ = core.ReadUInt32LE(r)
		if b != 1 {
			return errors.New(fmt.Sprintf("unsupported version of Auto-Reconnect packet"))
		}
		b, _ = core.ReadUInt32LE(r)
		s.LogonId = b
		s.Random, _ = core.ReadBytes(16, r)
	} else { // logon error info
		core.ReadUInt32LE(r)
		b, _ := core.ReadUInt32LE(r)
		b, _ = core.ReadUInt32LE(r)
		s.LogonId = b
	}
	core.ReadBytes(570, r)
	return err
}
func (s *SaveSessionInfo) Unpack(r io.Reader) (err error) {
	s.InfoType, err = core.ReadUInt32LE(r)
	switch s.InfoType {
	case INFOTYPE_LOGON:
		err = s.logonInfoV1(r)
	case INFOTYPE_LOGON_LONG:
		err = s.logonInfoV2(r)
	case INFOTYPE_LOGON_PLAINNOTIFY:
		err = s.logonPlainNotify(r)
	case INFOTYPE_LOGON_EXTENDED_INFO:
		err = s.logonInfoExtended(r)
	default:
		slog.Error(fmt.Sprintf("Unhandled saveSessionInfo type 0x%x", s.InfoType))
		return fmt.Errorf("Unhandled saveSessionInfo type 0x%x", s.InfoType)
	}

	return err
}

func (*SaveSessionInfo) Type2() uint8 {
	return PDUTYPE2_SAVE_SESSION_INFO
}

type PersistKeyPDU struct {
	NumEntriesCache0   uint16 `struc:"little"`
	NumEntriesCache1   uint16 `struc:"little"`
	NumEntriesCache2   uint16 `struc:"little"`
	NumEntriesCache3   uint16 `struc:"little"`
	NumEntriesCache4   uint16 `struc:"little"`
	TotalEntriesCache0 uint16 `struc:"little"`
	TotalEntriesCache1 uint16 `struc:"little"`
	TotalEntriesCache2 uint16 `struc:"little"`
	TotalEntriesCache3 uint16 `struc:"little"`
	TotalEntriesCache4 uint16 `struc:"little"`
	BBitMask           uint8  `struc:"little"`
	Pad1               uint8  `struc:"little"`
	Ppad3              uint16 `struc:"little"`
}

func (*PersistKeyPDU) Type2() uint8 {
	return PDUTYPE2_BITMAPCACHE_PERSISTENT_LIST
}

type UpdateData interface {
	FastPathUpdateType() uint8
	Unpack(io.Reader) error
}

type BitmapCompressedDataHeader struct {
	CbCompFirstRowSize uint16 `struc:"little"`
	CbCompMainBodySize uint16 `struc:"little"`
	CbScanWidth        uint16 `struc:"little"`
	CbUncompressedSize uint16 `struc:"little"`
}

type BitmapData struct {
	DestLeft         uint16 `struc:"little"`
	DestTop          uint16 `struc:"little"`
	DestRight        uint16 `struc:"little"`
	DestBottom       uint16 `struc:"little"`
	Width            uint16 `struc:"little"`
	Height           uint16 `struc:"little"`
	BitsPerPixel     uint16 `struc:"little"`
	Flags            uint16 `struc:"little"`
	BitmapLength     uint16 `struc:"little,sizeof=BitmapDataStream"`
	BitmapComprHdr   *BitmapCompressedDataHeader
	BitmapDataStream []byte
}

func (b *BitmapData) IsCompress() bool {
	return b.Flags&BITMAP_COMPRESSION != 0
}

type FastPathBitmapUpdateDataPDU struct {
	Header           uint16 `struc:"little"`
	NumberRectangles uint16 `struc:"little,sizeof=Rectangles"`
	Rectangles       []BitmapData
}

func (f *FastPathBitmapUpdateDataPDU) Unpack(r io.Reader) error {
	var err error
	f.Header, err = core.ReadUint16LE(r)
	f.NumberRectangles, err = core.ReadUint16LE(r)
	f.Rectangles = make([]BitmapData, 0, f.NumberRectangles)
	for i := 0; i < int(f.NumberRectangles); i++ {
		rect := BitmapData{}
		rect.DestLeft, err = core.ReadUint16LE(r)
		rect.DestTop, err = core.ReadUint16LE(r)
		rect.DestRight, err = core.ReadUint16LE(r)
		rect.DestBottom, err = core.ReadUint16LE(r)
		rect.Width, err = core.ReadUint16LE(r)
		rect.Height, err = core.ReadUint16LE(r)
		rect.BitsPerPixel, err = core.ReadUint16LE(r)
		rect.Flags, err = core.ReadUint16LE(r)
		rect.BitmapLength, err = core.ReadUint16LE(r)
		ln := rect.BitmapLength
		if rect.Flags&BITMAP_COMPRESSION != 0 && (rect.Flags&NO_BITMAP_COMPRESSION_HDR == 0) {
			rect.BitmapComprHdr = new(BitmapCompressedDataHeader)
			rect.BitmapComprHdr.CbCompFirstRowSize, err = core.ReadUint16LE(r)
			rect.BitmapComprHdr.CbCompMainBodySize, err = core.ReadUint16LE(r)
			rect.BitmapComprHdr.CbScanWidth, err = core.ReadUint16LE(r)
			rect.BitmapComprHdr.CbUncompressedSize, err = core.ReadUint16LE(r)
			ln = rect.BitmapComprHdr.CbCompMainBodySize
		}

		rect.BitmapDataStream, err = core.ReadBytes(int(ln), r)
		f.Rectangles = append(f.Rectangles, rect)
	}
	return err
}

func (*FastPathBitmapUpdateDataPDU) FastPathUpdateType() uint8 {
	return FASTPATH_UPDATETYPE_BITMAP
}

type FastPathColorPdu struct {
	CacheIdx uint16
	X        uint16
	Y        uint16
	Width    uint16
	Height   uint16
	MaskLen  uint16 `struc:"little,sizeof=Mask"`
	DataLen  uint16 `struc:"little,sizeof=Data"`
	Mask     []byte
	Data     []byte
}

func (*FastPathColorPdu) FastPathUpdateType() uint8 {
	return FASTPATH_UPDATETYPE_COLOR
}
func (f *FastPathColorPdu) Unpack(r io.Reader) error {
	return struc.Unpack(r, f)
}

type FastPathSurfaceCmds struct {
	Rects []BitmapData
}

func (*FastPathSurfaceCmds) FastPathUpdateType() uint8 {
	return FASTPATH_UPDATETYPE_SURFCMDS
}
func (f *FastPathSurfaceCmds) Unpack(r io.Reader) error {
	// This won't be called; Surface Commands are handled directly in RecvFastPath.
	return nil
}

// SurfaceCommandsResult holds parsed bitmap data and frame IDs to acknowledge.
type SurfaceCommandsResult struct {
	Rects    []BitmapData
	FrameIDs []uint32
}

// ParseSurfaceCommands parses one or more surface commands from raw data
// and returns decoded BitmapData rectangles and frame IDs that need acknowledgment.
func ParseSurfaceCommands(data []byte) SurfaceCommandsResult {
	r := bytes.NewReader(data)
	var result SurfaceCommandsResult
	for r.Len() > 0 {
		cmdType, err := core.ReadUint16LE(r)
		if err != nil {
			break
		}
		switch cmdType {
		case CMDTYPE_SET_SURFACE_BITS, CMDTYPE_STREAM_SURFACE_BITS:
			rect, err := decodeSurfaceBitsCmd(r)
			if err != nil {
				slog.Warn("decodeSurfaceBitsCmd", "err", err)
				return result
			}
			if rect != nil {
				result.Rects = append(result.Rects, *rect)
			}
		case CMDTYPE_FRAME_MARKER:
			frameAction, _ := core.ReadUint16LE(r)
			frameId, _ := core.ReadUInt32LE(r)
			if frameAction == SURFCMD_FRAMEACTION_END {
				result.FrameIDs = append(result.FrameIDs, frameId)
			}
		default:
			slog.Warn("Unknown surface command type", "cmdType", cmdType)
			return result
		}
	}
	return result
}

// decodeSurfaceBitsCmd parses a SET_SURFACE_BITS or STREAM_SURFACE_BITS command.
func decodeSurfaceBitsCmd(r io.Reader) (*BitmapData, error) {
	destLeft, err := core.ReadUint16LE(r)
	if err != nil {
		return nil, err
	}
	destTop, _ := core.ReadUint16LE(r)
	destRight, _ := core.ReadUint16LE(r)
	destBottom, _ := core.ReadUint16LE(r)

	// TS_BITMAP_DATA_EX
	bpp, _ := core.ReadUInt8(r)
	flags, _ := core.ReadUInt8(r)
	_, _ = core.ReadUInt8(r) // reserved
	codecID, _ := core.ReadUInt8(r)
	width, _ := core.ReadUint16LE(r)
	height, _ := core.ReadUint16LE(r)
	bitmapDataLength, _ := core.ReadUInt32LE(r)

	// Skip extended compressed bitmap header if present (24 bytes).
	// bitmapDataLength includes this header size when the flag is set.
	if flags&0x01 != 0 {
		core.ReadBytes(24, r)
		bitmapDataLength -= 24
	}

	bitmapData, err := core.ReadBytes(int(bitmapDataLength), r)
	if err != nil {
		return nil, fmt.Errorf("failed to read bitmap data: %v", err)
	}

	slog.Debug("decodeSurfaceBitsCmd",
		"dest", fmt.Sprintf("(%d,%d)-(%d,%d)", destLeft, destTop, destRight, destBottom),
		"size", fmt.Sprintf("%dx%d", width, height),
		"bpp", bpp, "codecID", codecID, "flags", flags, "dataLen", bitmapDataLength)

	var pixels []byte
	outBpp := uint16(bpp)
	switch codecID {
	case 0: // Uncompressed
		pixels = bitmapData
	case 1: // NSCodec
		pixels = decodeNSCodec(bitmapData, int(width), int(height))
		outBpp = 32 // NSCodec always decodes to BGRA (4 bytes/pixel)
	case 3: // RemoteFX (MS-RDPRFX)
		if DecodeRemoteFX != nil {
			pixels = DecodeRemoteFX(bitmapData, int(width), int(height))
			outBpp = 32
		} else {
			slog.Warn("RemoteFX surface codec not available", "codecID", codecID)
			return nil, nil
		}
	default:
		slog.Warn("Unsupported surface codec", "codecID", codecID)
		return nil, nil // skip unsupported codecs
	}

	if pixels == nil {
		return nil, nil
	}

	// Flip vertically for bottom-up codecs. NSCodec decodes top-down but the
	// bitmap coordinate system expects bottom-up. RFX (codecID=3) is already
	// in the correct top-down orientation and must NOT be flipped.
	if codecID != 3 {
		stride := int(width) * int(outBpp) / 8
		h := int(height)
		for y := 0; y < h/2; y++ {
			top := y * stride
			bot := (h - 1 - y) * stride
			for i := 0; i < stride; i++ {
				pixels[top+i], pixels[bot+i] = pixels[bot+i], pixels[top+i]
			}
		}
	}

	return &BitmapData{
		DestLeft:         destLeft,
		DestTop:          destTop,
		DestRight:        destRight,
		DestBottom:       destBottom,
		Width:            width,
		Height:           height,
		BitsPerPixel:     outBpp,
		Flags:            BITMAP_NO_PROCESSING,
		BitmapLength:     0,
		BitmapDataStream: pixels,
	}, nil
}

// decodeNSCodec decodes NSCodec (MS-RDPNSC) encoded bitmap data into BGRA pixels.
// Implements the decoder exactly as FreeRDP does (libfreerdp/codec/nsc.c).
func decodeNSCodec(data []byte, width, height int) []byte {
	if len(data) < 20 {
		slog.Warn("NSCodec data too short", "len", len(data))
		return nil
	}

	r := bytes.NewReader(data)
	lumaLen, _ := core.ReadUInt32LE(r)
	orangeLen, _ := core.ReadUInt32LE(r)
	greenLen, _ := core.ReadUInt32LE(r)
	alphaLen, _ := core.ReadUInt32LE(r)
	colorLossLevel, _ := core.ReadUInt8(r)
	chromaSubsamplingLevel, _ := core.ReadUInt8(r)
	_, _ = core.ReadUint16LE(r) // reserved

	if colorLossLevel < 1 {
		colorLossLevel = 1
	}
	shift := colorLossLevel - 1

	slog.Debug("NSCodec",
		"lumaLen", lumaLen, "orangeLen", orangeLen,
		"greenLen", greenLen, "alphaLen", alphaLen,
		"colorLossLevel", colorLossLevel,
		"chromaSub", chromaSubsamplingLevel)

	remaining := data[20:]

	// Bounds check
	totalPlaneLen := int(lumaLen + orangeLen + greenLen + alphaLen)
	if totalPlaneLen > len(remaining) {
		slog.Warn("NSCodec plane lengths exceed data",
			"planeLens", totalPlaneLen, "available", len(remaining))
		return nil
	}

	// Compute plane original (decompressed) sizes, matching FreeRDP:
	// Y and A: tempWidth * height (Y uses rounded width for row stride)
	// Co and Cg: (tempWidth>>1) * (tempHeight>>1) when chroma subsampled
	tempWidth := (width + 7) &^ 7   // ROUND_UP_TO(width, 8)
	tempHeight := (height + 1) &^ 1 // ROUND_UP_TO(height, 2)

	var yOrigSize, coOrigSize, cgOrigSize, aOrigSize int
	if chromaSubsamplingLevel > 0 {
		yOrigSize = tempWidth * height
		coOrigSize = (tempWidth >> 1) * (tempHeight >> 1)
		cgOrigSize = coOrigSize
	} else {
		yOrigSize = width * height
		coOrigSize = yOrigSize
		cgOrigSize = yOrigSize
	}
	aOrigSize = width * height

	// Decompress each plane: if planeSize < originalSize → NRLE decode,
	// if planeSize == 0 → fill with 0xFF, otherwise raw copy.
	yPlane := nscDecompressPlane(remaining[:lumaLen], int(lumaLen), yOrigSize)
	remaining = remaining[lumaLen:]
	coPlane := nscDecompressPlane(remaining[:orangeLen], int(orangeLen), coOrigSize)
	remaining = remaining[orangeLen:]
	cgPlane := nscDecompressPlane(remaining[:greenLen], int(greenLen), cgOrigSize)
	remaining = remaining[greenLen:]

	var aPlane []byte
	if alphaLen > 0 {
		aPlane = nscDecompressPlane(remaining[:alphaLen], int(alphaLen), aOrigSize)
	}

	// YCoCg to BGRA conversion (matches FreeRDP nsc_decode exactly).
	// FreeRDP formula:
	//   co_val = (INT16)(INT8)(((INT16)*coplane) << shift)
	//   cg_val = (INT16)(INT8)(((INT16)*cgplane) << shift)
	//   R = Y + co - cg
	//   G = Y + cg
	//   B = Y - co - cg
	totalPixels := width * height
	pixels := make([]byte, totalPixels*4)

	// Row widths for plane indexing (FreeRDP uses rw for Y, rw>>1 for chroma)
	yRowWidth := width
	coRowWidth := width
	if chromaSubsamplingLevel > 0 {
		yRowWidth = tempWidth
		coRowWidth = tempWidth >> 1
	}

	for py := 0; py < height; py++ {
		yRowOff := py * yRowWidth
		var coRowOff, cgRowOff int
		if chromaSubsamplingLevel > 0 {
			coRowOff = (py >> 1) * coRowWidth
			cgRowOff = coRowOff
		} else {
			coRowOff = py * coRowWidth
			cgRowOff = coRowOff
		}

		coIdx := coRowOff
		cgIdx := cgRowOff

		for px := 0; px < width; px++ {
			outIdx := py*width + px
			yIdx := yRowOff + px

			yVal := int16(0)
			if yIdx < len(yPlane) {
				yVal = int16(yPlane[yIdx])
			}

			coVal := int16(0)
			cgVal := int16(0)
			if coIdx < len(coPlane) {
				coVal = int16(int8(byte(int16(coPlane[coIdx]) << shift)))
			}
			if cgIdx < len(cgPlane) {
				cgVal = int16(int8(byte(int16(cgPlane[cgIdx]) << shift)))
			}

			rv := yVal + coVal - cgVal
			gv := yVal + cgVal
			bv := yVal - coVal - cgVal

			off := outIdx * 4
			pixels[off+0] = clampByte(bv)
			pixels[off+1] = clampByte(gv)
			pixels[off+2] = clampByte(rv)
			if aPlane != nil && outIdx < len(aPlane) {
				pixels[off+3] = aPlane[outIdx]
			} else {
				pixels[off+3] = 0xFF
			}

			// Advance chroma pointer (FreeRDP: coplane += chromaSub ? x%2 : 1)
			if chromaSubsamplingLevel > 0 {
				coIdx += px % 2
				cgIdx += px % 2
			} else {
				coIdx++
				cgIdx++
			}
		}
	}

	return pixels
}

func clampByte(v int16) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// nscDecompressPlane decompresses a single NSCodec plane.
// If planeSize == 0, fills with 0xFF. If planeSize >= originalSize, raw copy.
// Otherwise, uses the NRLE format (matching FreeRDP's nsc_rle_decode).
func nscDecompressPlane(input []byte, planeSize, originalSize int) []byte {
	if planeSize == 0 {
		out := make([]byte, originalSize)
		for i := range out {
			out[i] = 0xFF
		}
		return out
	}
	if planeSize >= originalSize {
		out := make([]byte, originalSize)
		copy(out, input[:originalSize])
		return out
	}
	return nrleDecode(input[:planeSize], originalSize)
}

// nrleDecode decompresses NRLE (NSCodec Run-Length Encoding) data.
// Matches FreeRDP's nsc_rle_decode exactly:
//   - 2 consecutive equal bytes trigger a run
//   - If 3rd byte < 0xFF: run length = byte + 2
//   - If 3rd byte == 0xFF: run length = next 4 bytes as uint32 LE
//   - Last 4 bytes of output are copied raw from input
func nrleDecode(input []byte, originalSize int) []byte {
	output := make([]byte, originalSize)
	left := originalSize
	inPos := 0
	outPos := 0

	for left > 4 && inPos < len(input) {
		value := input[inPos]
		inPos++

		if left == 5 {
			output[outPos] = value
			outPos++
			left--
		} else if inPos < len(input) && value == input[inPos] {
			// Run detected
			inPos++ // skip the second occurrence
			runLen := 0
			if inPos < len(input) {
				if input[inPos] < 0xFF {
					runLen = int(input[inPos]) + 2
					inPos++
				} else {
					// Long run: skip 0xFF marker, read uint32 LE
					inPos++
					if inPos+4 <= len(input) {
						runLen = int(input[inPos]) |
							int(input[inPos+1])<<8 |
							int(input[inPos+2])<<16 |
							int(input[inPos+3])<<24
						inPos += 4
					}
				}
			}
			if runLen > left {
				runLen = left
			}
			for i := 0; i < runLen && outPos < originalSize; i++ {
				output[outPos] = value
				outPos++
			}
			left -= runLen
		} else {
			// Single byte
			output[outPos] = value
			outPos++
			left--
		}
	}

	// Copy last 4 bytes raw
	if left >= 4 && inPos+4 <= len(input) {
		copy(output[outPos:outPos+4], input[inPos:inPos+4])
	}

	return output
}

type FastPathUpdatePointerPDU struct {
	XorBpp   uint16 `struc:"little"`
	CacheIdx uint16 `struc:"little"`
	X        uint16 `struc:"little"`
	Y        uint16 `struc:"little"`
	Width    uint16 `struc:"little"`
	Height   uint16 `struc:"little"`
	MaskLen  uint16 `struc:"little,sizeof=Mask"` // lengthAndMask
	DataLen  uint16 `struc:"little,sizeof=Data"` // lengthXorMask
	Data     []byte // xorMaskData
	Mask     []byte // andMaskData
}

func (*FastPathUpdatePointerPDU) FastPathUpdateType() uint8 {
	return FASTPATH_UPDATETYPE_POINTER
}

func (f *FastPathUpdatePointerPDU) Unpack(r io.Reader) error {
	return struc.Unpack(r, f)
}

type FastPathUpdatePointerNullPDU struct {
}

func (*FastPathUpdatePointerNullPDU) FastPathUpdateType() uint8 {
	return FASTPATH_UPDATETYPE_PTR_NULL
}
func (f *FastPathUpdatePointerNullPDU) Unpack(r io.Reader) error {
	return nil
}

type FastPathUpdateCachedPDU struct {
	CacheIdx uint16 `struc:"little"`
}

func (*FastPathUpdateCachedPDU) FastPathUpdateType() uint8 {
	return FASTPATH_UPDATETYPE_CACHED
}

func (f *FastPathUpdateCachedPDU) Unpack(r io.Reader) error {
	return struc.Unpack(r, f)
}

type FastPathUpdatePDU struct {
	UpdateHeader     uint8
	Fragmentation    uint8
	CompressionFlags uint8
	Size             uint16
	Data             UpdateData
}

const (
	FASTPATH_OUTPUT_COMPRESSION_USED = 0x2
)

const (
	FASTPATH_FRAGMENT_SINGLE = (0x0 << 4)
	FASTPATH_FRAGMENT_LAST   = (0x1 << 4)
	FASTPATH_FRAGMENT_FIRST  = (0x2 << 4)
	FASTPATH_FRAGMENT_NEXT   = (0x3 << 4)
)

func readFastPathUpdatePDU(r io.Reader, code uint8) (*FastPathUpdatePDU, error) {
	f := &FastPathUpdatePDU{}
	var err error
	var d UpdateData
	switch code {
	case FASTPATH_UPDATETYPE_ORDERS:
		d = &FastPathOrdersPDU{}
	case FASTPATH_UPDATETYPE_BITMAP:
		d = &FastPathBitmapUpdateDataPDU{}
	case FASTPATH_UPDATETYPE_PALETTE:
	case FASTPATH_UPDATETYPE_SYNCHRONIZE:
	case FASTPATH_UPDATETYPE_SURFCMDS:
		//d = &FastPathSurfaceCmds{}
	case FASTPATH_UPDATETYPE_PTR_NULL:
		d = &FastPathUpdatePointerNullPDU{}
	case FASTPATH_UPDATETYPE_PTR_DEFAULT:
	case FASTPATH_UPDATETYPE_PTR_POSITION:
	case FASTPATH_UPDATETYPE_COLOR:
		//d = &FastPathColorPdu{}
	case FASTPATH_UPDATETYPE_CACHED:
		d = &FastPathUpdateCachedPDU{}
	case FASTPATH_UPDATETYPE_POINTER:
		d = &FastPathUpdatePointerPDU{}
	case FASTPATH_UPDATETYPE_LARGE_POINTER:
	default:
		return f, errors.New(fmt.Sprintf("Unknown FastPathPDU type 0x%x", code))
	}
	if d != nil {
		err = d.Unpack(r)
		if err != nil {
			//slog.Error("Unpack:", err)
			return nil, err
		}
	} else {
		return nil, errors.New(fmt.Sprintf("Unsupport FastPathPDU type 0x%x", code))
	}

	f.Data = d
	return f, nil
}

type ShareControlHeader struct {
	TotalLength uint16 `struc:"little"`
	PDUType     uint16 `struc:"little"`
	PDUSource   uint16 `struc:"little"`
}

type PDU struct {
	ShareCtrlHeader *ShareControlHeader
	Message         PDUMessage
}

func NewPDU(userId uint16, message PDUMessage) *PDU {
	pdu := &PDU{}
	pdu.ShareCtrlHeader = &ShareControlHeader{
		TotalLength: uint16(len(message.Serialize()) + 6),
		PDUType:     message.Type(),
		PDUSource:   userId,
	}
	pdu.Message = message
	return pdu
}

func readPDU(r io.Reader) (*PDU, error) {
	pdu := &PDU{}
	var err error
	header := &ShareControlHeader{}
	err = struc.Unpack(r, header)
	if err != nil {
		return nil, err
	}

	pdu.ShareCtrlHeader = header

	var d PDUMessage
	switch pdu.ShareCtrlHeader.PDUType {
	case PDUTYPE_DEMANDACTIVEPDU:
		slog.Debug("readPDU:PDUTYPE_DEMANDACTIVEPDU")
		d, err = readDemandActivePDU(r)
	case PDUTYPE_DATAPDU:
		slog.Debug("readPDU:PDUTYPE_DATAPDU")
		d, err = readDataPDU(r)
	case PDUTYPE_CONFIRMACTIVEPDU:
		slog.Debug("readPDU:PDUTYPE_CONFIRMACTIVEPDU")
		d, err = readConfirmActivePDU(r)
	case PDUTYPE_DEACTIVATEALLPDU:
		slog.Debug("readPDU:PDUTYPE_DEACTIVATEALLPDU")
		d, err = readDeactiveAllPDU(r)
	case PDUTYPE_SERVER_REDIR_PKT:
		slog.Debug("readPDU:PDUTYPE_SERVER_REDIR_PKT")
		d, err = readServerRedirectionPDU(r)
	default:
		slog.Error(fmt.Sprintf("PDU invalid pdu type: 0x%02x", pdu.ShareCtrlHeader.PDUType))
	}
	if err != nil {
		return nil, err
	}
	pdu.Message = d
	return pdu, err
}

func (p *PDU) serialize() []byte {
	buff := &bytes.Buffer{}
	struc.Pack(buff, p.ShareCtrlHeader)
	core.WriteBytes(p.Message.Serialize(), buff)
	return buff.Bytes()
}

type SlowPathInputEvent struct {
	EventTime         uint32 `struc:"little"`
	MessageType       uint16 `struc:"little"`
	Size              int    `struc:"skip"`
	SlowPathInputData []byte `struc:"sizefrom=Size"`
}

type PointerEvent struct {
	PointerFlags uint16 `struc:"little"`
	XPos         uint16 `struc:"little"`
	YPos         uint16 `struc:"little"`
}

func (p *PointerEvent) Serialize() []byte {
	return []byte{
		byte(p.PointerFlags), byte(p.PointerFlags >> 8),
		byte(p.XPos), byte(p.XPos >> 8),
		byte(p.YPos), byte(p.YPos >> 8),
	}
}

// FastPathEncode appends this mouse event in the Fast-Path Input wire format
// (MS-RDPBCGR §2.2.8.1.2.2.3) to buf and returns the new slice.
func (p *PointerEvent) FastPathEncode(buf []byte) []byte {
	buf = append(buf, byte(FASTPATH_INPUT_EVENT_MOUSE<<5))
	buf = append(buf,
		byte(p.PointerFlags), byte(p.PointerFlags>>8),
		byte(p.XPos), byte(p.XPos>>8),
		byte(p.YPos), byte(p.YPos>>8))
	return buf
}

type SynchronizeEvent struct {
	Pad2Octets  uint16 `struc:"little"`
	ToggleFlags uint32 `struc:"little"`
}

func (p *SynchronizeEvent) Serialize() []byte {
	return []byte{
		byte(p.Pad2Octets), byte(p.Pad2Octets >> 8),
		byte(p.ToggleFlags), byte(p.ToggleFlags >> 8),
		byte(p.ToggleFlags >> 16), byte(p.ToggleFlags >> 24),
	}
}

type ScancodeKeyEvent struct {
	KeyboardFlags uint16 `struc:"little"`
	KeyCode       uint16 `struc:"little"`
	Pad2Octets    uint16 `struc:"little"`
}

func (p *ScancodeKeyEvent) Serialize() []byte {
	return []byte{
		byte(p.KeyboardFlags), byte(p.KeyboardFlags >> 8),
		byte(p.KeyCode), byte(p.KeyCode >> 8),
		byte(p.Pad2Octets), byte(p.Pad2Octets >> 8),
	}
}

// FastPathEncode appends this scancode event in the Fast-Path Input wire
// format (MS-RDPBCGR §2.2.8.1.2.2.1) to buf and returns the new slice.
//
// Slow-path callers in this codebase historically encoded extended keys by
// stuffing the 0xE0 prefix into the high byte of KeyCode (e.g. 0xE048 for
// the up-arrow) and leaving KBDFLAGS_EXTENDED unset.  Fast-path can only
// carry an 8-bit make code, so we promote any 0xE0XX encoding to the proper
// EXTENDED flag here.
func (p *ScancodeKeyEvent) FastPathEncode(buf []byte) []byte {
	flags := byte(0)
	if p.KeyboardFlags&KBDFLAGS_RELEASE != 0 {
		flags |= FASTPATH_INPUT_KBDFLAGS_RELEASE
	}
	if p.KeyboardFlags&KBDFLAGS_EXTENDED != 0 || p.KeyCode&0xFF00 == 0xE000 {
		flags |= FASTPATH_INPUT_KBDFLAGS_EXTENDED
	}
	if p.KeyboardFlags&KBDFLAGS_EXTENDED1 != 0 {
		flags |= FASTPATH_INPUT_KBDFLAGS_EXTENDED1
	}
	buf = append(buf, byte(FASTPATH_INPUT_EVENT_SCANCODE<<5)|flags)
	buf = append(buf, byte(p.KeyCode))
	return buf
}

type UnicodeKeyEvent struct {
	KeyboardFlags uint16 `struc:"little"`
	Unicode       uint16 `struc:"little"`
	Pad2Octets    uint16 `struc:"little"`
}

func (p *UnicodeKeyEvent) Serialize() []byte {
	return []byte{
		byte(p.KeyboardFlags), byte(p.KeyboardFlags >> 8),
		byte(p.Unicode), byte(p.Unicode >> 8),
		byte(p.Pad2Octets), byte(p.Pad2Octets >> 8),
	}
}

// FastPathEncode appends this unicode key event in the Fast-Path Input wire
// format (MS-RDPBCGR §2.2.8.1.2.2.5) to buf and returns the new slice.
func (p *UnicodeKeyEvent) FastPathEncode(buf []byte) []byte {
	flags := byte(0)
	if p.KeyboardFlags&KBDFLAGS_RELEASE != 0 {
		flags |= FASTPATH_INPUT_KBDFLAGS_RELEASE
	}
	buf = append(buf, byte(FASTPATH_INPUT_EVENT_UNICODE<<5)|flags)
	buf = append(buf, byte(p.Unicode), byte(p.Unicode>>8))
	return buf
}

type ClientInputEventPDU struct {
	NumEvents           uint16               `struc:"little,sizeof=SlowPathInputEvents"`
	Pad2Octets          uint16               `struc:"little"`
	SlowPathInputEvents []SlowPathInputEvent `struc:"little"`
}

func (*ClientInputEventPDU) Type2() uint8 {
	return PDUTYPE2_INPUT
}
func (*ClientInputEventPDU) Unpack(io.Reader) error {
	return nil
}
