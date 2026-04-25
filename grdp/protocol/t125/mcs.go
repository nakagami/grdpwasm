package t125

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"time"

	//	"github.com/nakagami/grdp/plugin/cliprdr"
	"github.com/nakagami/grdp/plugin/drdynvc"
	"github.com/nakagami/grdp/plugin/rail"
	"github.com/nakagami/grdp/plugin/rdpsnd"

	"github.com/nakagami/grdp/core"
	"github.com/nakagami/grdp/emission"
	"github.com/nakagami/grdp/protocol/t125/ber"
	"github.com/nakagami/grdp/protocol/t125/gcc"
	"github.com/nakagami/grdp/protocol/t125/per"
)

// take idea from https://github.com/Madnikulin50/gordp

// Multiple Channel Service layer

type MCSMessage uint8

const (
	MCS_TYPE_CONNECT_INITIAL  MCSMessage = 0x65
	MCS_TYPE_CONNECT_RESPONSE            = 0x66
)

type MCSDomainPDU uint16

const (
	ERECT_DOMAIN_REQUEST          MCSDomainPDU = 1
	DISCONNECT_PROVIDER_ULTIMATUM              = 8
	ATTACH_USER_REQUEST                        = 10
	ATTACH_USER_CONFIRM                        = 11
	CHANNEL_JOIN_REQUEST                       = 14
	CHANNEL_JOIN_CONFIRM                       = 15
	SEND_DATA_REQUEST                          = 25
	SEND_DATA_INDICATION                       = 26
)

const (
	MCS_GLOBAL_CHANNEL_ID uint16 = 1003
	MCS_USERCHANNEL_BASE         = 1001
)

const (
	GLOBAL_CHANNEL_NAME = "global"
)

/**
 * Format MCS PDULayer header packet
 * @param mcsPdu {integer}
 * @param options {integer}
 * @returns {type.UInt8} headers
 */
func writeMCSPDUHeader(mcsPdu MCSDomainPDU, options uint8, w io.Writer) {
	core.WriteUInt8((uint8(mcsPdu)<<2)|options, w)
}

func readMCSPDUHeader(options uint8, mcsPdu MCSDomainPDU) bool {
	return (options >> 2) == uint8(mcsPdu)
}

type DomainParameters struct {
	MaxChannelIds   int
	MaxUserIds      int
	MaxTokenIds     int
	NumPriorities   int
	MinThoughput    int
	MaxHeight       int
	MaxMCSPDUsize   int
	ProtocolVersion int
}

/**
 * @see http://www.itu.int/rec/T-REC-T.125-199802-I/en page 25
 * @returns {asn1.univ.Sequence}
 */
func NewDomainParameters(
	maxChannelIds int,
	maxUserIds int,
	maxTokenIds int,
	numPriorities int,
	minThoughput int,
	maxHeight int,
	maxMCSPDUsize int,
	protocolVersion int) *DomainParameters {
	return &DomainParameters{maxChannelIds, maxUserIds, maxTokenIds,
		numPriorities, minThoughput, maxHeight, maxMCSPDUsize, protocolVersion}
}

func (d *DomainParameters) BER() []byte {
	buff := &bytes.Buffer{}
	ber.WriteInteger(d.MaxChannelIds, buff)
	ber.WriteInteger(d.MaxUserIds, buff)
	ber.WriteInteger(d.MaxTokenIds, buff)
	ber.WriteInteger(1, buff)
	ber.WriteInteger(0, buff)
	ber.WriteInteger(1, buff)
	ber.WriteInteger(d.MaxMCSPDUsize, buff)
	ber.WriteInteger(2, buff)
	return buff.Bytes()
}

func ReadDomainParameters(r io.Reader) (*DomainParameters, error) {
	if !ber.ReadUniversalTag(ber.TAG_SEQUENCE, true, r) {
		return nil, errors.New("bad BER tags")
	}
	d := &DomainParameters{}
	ber.ReadLength(r)

	d.MaxChannelIds, _ = ber.ReadInteger(r)
	d.MaxUserIds, _ = ber.ReadInteger(r)
	d.MaxTokenIds, _ = ber.ReadInteger(r)
	ber.ReadInteger(r)
	ber.ReadInteger(r)
	ber.ReadInteger(r)
	d.MaxMCSPDUsize, _ = ber.ReadInteger(r)
	ber.ReadInteger(r)
	return d, nil
}

/**
 * @see http://www.itu.int/rec/T-REC-T.125-199802-I/en page 25
 * @param userData {Buffer}
 * @returns {asn1.univ.Sequence}
 */
type ConnectInitial struct {
	CallingDomainSelector []byte
	CalledDomainSelector  []byte
	UpwardFlag            bool
	TargetParameters      DomainParameters
	MinimumParameters     DomainParameters
	MaximumParameters     DomainParameters
	UserData              []byte
}

func NewConnectInitial(userData []byte) ConnectInitial {
	return ConnectInitial{[]byte{0x1},
		[]byte{0x1},
		true,
		*NewDomainParameters(34, 2, 0, 1, 0, 1, 0xffff, 2),
		*NewDomainParameters(1, 1, 1, 1, 0, 1, 0x420, 2),
		*NewDomainParameters(0xffff, 0xfc17, 0xffff, 1, 0, 1, 0xffff, 2),
		userData}
}

func (c *ConnectInitial) BER() []byte {
	buff := &bytes.Buffer{}
	ber.WriteOctetstring(string(c.CallingDomainSelector), buff)
	ber.WriteOctetstring(string(c.CalledDomainSelector), buff)
	ber.WriteBoolean(c.UpwardFlag, buff)
	ber.WriteEncodedDomainParams(c.TargetParameters.BER(), buff)
	ber.WriteEncodedDomainParams(c.MinimumParameters.BER(), buff)
	ber.WriteEncodedDomainParams(c.MaximumParameters.BER(), buff)
	ber.WriteOctetstring(string(c.UserData), buff)
	return buff.Bytes()
}

/**
 * @see http://www.itu.int/rec/T-REC-T.125-199802-I/en page 25
 * @returns {asn1.univ.Sequence}
 */

type ConnectResponse struct {
	result           uint8
	calledConnectId  int
	domainParameters *DomainParameters
	userData         []byte
}

func NewConnectResponse(userData []byte) *ConnectResponse {
	return &ConnectResponse{0,
		0,
		NewDomainParameters(22, 3, 0, 1, 0, 1, 0xfff8, 2),
		userData}
}

func ReadConnectResponse(r io.Reader) (*ConnectResponse, error) {
	c := &ConnectResponse{}
	var err error
	_, err = ber.ReadApplicationTag(MCS_TYPE_CONNECT_RESPONSE, r)
	if err != nil {
		return nil, err
	}
	c.result, err = ber.ReadEnumerated(r)
	if err != nil {
		return nil, err
	}

	c.calledConnectId, err = ber.ReadInteger(r)
	c.domainParameters, err = ReadDomainParameters(r)
	if err != nil {
		return nil, err
	}
	if !ber.ReadUniversalTag(ber.TAG_OCTET_STRING, false, r) {
		return nil, errors.New("invalid expected BER tag")
	}
	dataLen, _ := ber.ReadLength(r)
	c.userData, err = core.ReadBytes(dataLen, r)
	return c, err
}

type MCSChannelInfo struct {
	ID   uint16
	Name string
}

type MCS struct {
	emission.Emitter
	transport  core.Transport
	recvOpCode MCSDomainPDU
	sendOpCode MCSDomainPDU
	channels   []MCSChannelInfo
}

func NewMCS(t core.Transport, recvOpCode MCSDomainPDU, sendOpCode MCSDomainPDU) *MCS {
	m := &MCS{
		*emission.NewEmitter(),
		t,
		recvOpCode,
		sendOpCode,
		[]MCSChannelInfo{{MCS_GLOBAL_CHANNEL_ID, GLOBAL_CHANNEL_NAME}},
	}

	m.transport.On("close", func() {
		m.Emit("close")
	}).On("error", func(err error) {
		m.Emit("error", err)
	})
	return m
}

func (x *MCS) Read(b []byte) (n int, err error) {
	return x.transport.Read(b)
}

func (x *MCS) Write(b []byte) (n int, err error) {
	return x.transport.Write(b)
}

func (m *MCS) Close() error {
	return m.transport.Close()
}

type MCSClient struct {
	*MCS
	clientCoreData     *gcc.ClientCoreData
	clientNetworkData  *gcc.ClientNetworkData
	clientSecurityData *gcc.ClientSecurityData

	serverCoreData     *gcc.ServerCoreData
	serverNetworkData  *gcc.ServerNetworkData
	serverSecurityData *gcc.ServerSecurityData

	channelsConnected    int
	userId               uint16
	nbChannelRequested   int
	messageChannelId     uint16 // from SC_MCS_MSGCHANNEL; 0 = not negotiated
	messageChannelJoined bool
	bwStartTime          time.Time // timestamp of last RDP_BW_START for timeDelta calculation
}

func NewMCSClient(t core.Transport, kbdLayout uint32, keyboardType uint32, keyboardSubType uint32) *MCSClient {
	c := &MCSClient{
		MCS:                NewMCS(t, SEND_DATA_INDICATION, SEND_DATA_REQUEST),
		clientCoreData:     gcc.NewClientCoreData(kbdLayout, keyboardType, keyboardSubType),
		clientNetworkData:  gcc.NewClientNetworkData(),
		clientSecurityData: gcc.NewClientSecurityData(),
		userId:             1 + MCS_USERCHANNEL_BASE,
	}
	c.transport.On("connect", c.connect)
	return c
}

func (c *MCSClient) SetClientDesktop(width, height uint16) {
	c.clientCoreData.DesktopWidth = width
	c.clientCoreData.DesktopHeight = height
}

func (c *MCSClient) SetClientDynvcProtocol() {
	c.clientCoreData.EarlyCapabilityFlags |= gcc.RNS_UD_CS_SUPPORT_DYNVC_GFX_PROTOCOL
	c.clientNetworkData.AddVirtualChannel(drdynvc.ChannelName, drdynvc.ChannelOption)
}

func (c *MCSClient) SetClientRemoteProgram() {
	c.clientNetworkData.AddVirtualChannel(rail.ChannelName, rail.ChannelOption)
}

func (c *MCSClient) SetClientSoundProtocol() {
	c.clientNetworkData.AddVirtualChannel(rdpsnd.ChannelName, rdpsnd.ChannelOption)
}

func (c *MCSClient) SetClientDeviceRedirection() {
	c.clientNetworkData.AddVirtualChannel("rdpdr",
		uint32(gcc.CHANNEL_OPTION_INITIALIZED|gcc.CHANNEL_OPTION_ENCRYPT_RDP|gcc.CHANNEL_OPTION_COMPRESS_RDP))
}

func (c *MCSClient) SetClientClipboard() {
	c.clientNetworkData.AddVirtualChannel("cliprdr",
		uint32(gcc.CHANNEL_OPTION_INITIALIZED|gcc.CHANNEL_OPTION_ENCRYPT_RDP|gcc.CHANNEL_OPTION_COMPRESS_RDP))
}

func (c *MCSClient) connect(selectedProtocol uint32) {
	slog.Debug("connect", "selectedProtocol", selectedProtocol)
	c.clientCoreData.ServerSelectedProtocol = selectedProtocol

	slog.Debug("connnect", "clientCoreData", c.clientCoreData)
	slog.Debug("connect", "clientNetworkData", c.clientNetworkData)
	slog.Debug("connect", "clientSecurityData", c.clientSecurityData)
	// sendConnectclientCoreDataInitial
	userDataBuff := bytes.Buffer{}
	userDataBuff.Write(c.clientCoreData.Pack())
	userDataBuff.Write(c.clientNetworkData.Pack())
	userDataBuff.Write(c.clientSecurityData.Pack())
	userDataBuff.Write(gcc.PackClientMsgChannelData())

	slog.Debug("userData", "data", core.Hex(userDataBuff.Bytes()), "len", len(userDataBuff.Bytes()))
	ccReq := gcc.MakeConferenceCreateRequest(userDataBuff.Bytes())
	slog.Debug("ccReq", "data", core.Hex(ccReq), "len", len(ccReq))
	connectInitial := NewConnectInitial(ccReq)
	connectInitialBerEncoded := connectInitial.BER()

	dataBuff := &bytes.Buffer{}
	ber.WriteApplicationTag(uint8(MCS_TYPE_CONNECT_INITIAL), len(connectInitialBerEncoded), dataBuff)
	dataBuff.Write(connectInitialBerEncoded)
	slog.Debug("send connet initial", "data", core.Hex(dataBuff.Bytes()), "len", len(dataBuff.Bytes()))

	_, err := c.transport.Write(dataBuff.Bytes())
	if err != nil {
		c.Emit("error", errors.New(fmt.Sprintf("mcs sendConnectInitial write error %v", err)))
		return
	}
	slog.Debug("mcs wait for data event")
	c.transport.Once("data", c.recvConnectResponse)
}

func (c *MCSClient) recvConnectResponse(s []byte) {
	slog.Debug("mcs recvConnectResponse", "s", core.Hex(s))
	cResp, err := ReadConnectResponse(bytes.NewReader(s))
	if err != nil {
		c.Emit("error", errors.New(fmt.Sprintf("ReadConnectResponse %v", err)))
		return
	}
	// record server gcc block
	serverSettings := gcc.ReadConferenceCreateResponse(cResp.userData)
	for _, v := range serverSettings {
		switch v.(type) {
		case *gcc.ServerSecurityData:
			c.serverSecurityData = v.(*gcc.ServerSecurityData)

		case *gcc.ServerCoreData:
			c.serverCoreData = v.(*gcc.ServerCoreData)

		case *gcc.ServerNetworkData:
			c.serverNetworkData = v.(*gcc.ServerNetworkData)

		case *gcc.ServerMsgChannelData:
			c.messageChannelId = v.(*gcc.ServerMsgChannelData).MCSChannelId
			slog.Debug("SC_MCS_MSGCHANNEL", "messageChannelId", c.messageChannelId)

		default:
			slog.Warn("recvConnectResponse: unhandled server gcc block", "type", reflect.TypeOf(v))
		}
	}
	c.sendErectDomainRequest()
	c.sendAttachUserRequest()

	c.transport.Once("data", c.recvAttachUserConfirm)
}

func (c *MCSClient) sendErectDomainRequest() {
	buff := &bytes.Buffer{}
	writeMCSPDUHeader(ERECT_DOMAIN_REQUEST, 0, buff)
	per.WriteInteger(0, buff)
	per.WriteInteger(0, buff)
	c.transport.Write(buff.Bytes())
}

func (c *MCSClient) sendAttachUserRequest() {
	buff := &bytes.Buffer{}
	writeMCSPDUHeader(ATTACH_USER_REQUEST, 0, buff)
	c.transport.Write(buff.Bytes())
}

func (c *MCSClient) recvAttachUserConfirm(s []byte) {
	slog.Debug("mcs recvAttachUserConfirm", "s", core.Hex(s))
	r := bytes.NewReader(s)

	option, err := core.ReadUInt8(r)
	if err != nil {
		c.Emit("error", err)
		return
	}

	if !readMCSPDUHeader(option, ATTACH_USER_CONFIRM) {
		c.Emit("error", errors.New("NODE_RDP_PROTOCOL_T125_MCS_BAD_HEADER"))
		return
	}

	e, err := per.ReadEnumerates(r)
	if err != nil {
		c.Emit("error", err)
		return
	}
	if e != 0 {
		c.Emit("error", errors.New("NODE_RDP_PROTOCOL_T125_MCS_SERVER_REJECT_USER'"))
		return
	}

	userId, _ := per.ReadInteger16(r)
	userId += MCS_USERCHANNEL_BASE
	c.userId = userId

	c.channels = append(c.channels, MCSChannelInfo{userId, "user"})
	c.connectChannels()
}

func (c *MCSClient) connectChannels() {
	slog.Debug("connectChannels", "channelsConnected", c.channelsConnected, "channels", c.channels)
	if c.channelsConnected >= len(c.channels) {
		// Join the message channel (for connect-time auto-detection) before SVCs.
		if c.messageChannelId != 0 && !c.messageChannelJoined {
			c.messageChannelJoined = true
			c.sendChannelJoinRequest(c.messageChannelId)
			c.transport.Once("data", c.recvChannelJoinConfirm)
			return
		}

		if c.nbChannelRequested < int(c.serverNetworkData.ChannelCount) {
			//static virtual channel
			chanId := c.serverNetworkData.ChannelIdArray[c.nbChannelRequested]
			c.nbChannelRequested++
			c.sendChannelJoinRequest(chanId)
			c.transport.Once("data", c.recvChannelJoinConfirm)
			return
		}
		c.transport.On("data", c.recvData)
		// send client and sever gcc informations callback to sec
		clientData := make([]interface{}, 0)
		clientData = append(clientData, c.clientCoreData)
		clientData = append(clientData, c.clientSecurityData)
		clientData = append(clientData, c.clientNetworkData)

		serverData := make([]interface{}, 0)
		serverData = append(serverData, c.serverCoreData)
		serverData = append(serverData, c.serverSecurityData)
		c.Emit("connect", clientData, serverData, c.userId, c.channels)
		return
	}

	// sendChannelJoinRequest
	c.sendChannelJoinRequest(c.channels[c.channelsConnected].ID)

	c.transport.Once("data", c.recvChannelJoinConfirm)
}

func (c *MCSClient) sendChannelJoinRequest(channelId uint16) {
	slog.Debug("sendChannelJoinRequest", "channelId", channelId)
	buff := &bytes.Buffer{}
	writeMCSPDUHeader(CHANNEL_JOIN_REQUEST, 0, buff)
	per.WriteInteger16(c.userId-MCS_USERCHANNEL_BASE, buff)
	per.WriteInteger16(channelId, buff)
	c.transport.Write(buff.Bytes())
}

func (c *MCSClient) recvData(s []byte) {
	slog.Debug("mcs recvData", "s", core.Hex(s))

	r := bytes.NewReader(s)
	option, err := core.ReadUInt8(r)
	if err != nil {
		c.Emit("error", err)
		return
	}

	if readMCSPDUHeader(option, DISCONNECT_PROVIDER_ULTIMATUM) {
		c.Emit("error", errors.New("MCS DISCONNECT_PROVIDER_ULTIMATUM"))
		c.transport.Close()
		return
	} else if !readMCSPDUHeader(option, c.recvOpCode) {
		c.Emit("error", errors.New("Invalid expected MCS opcode receive data"))
		return
	}

	userId, _ := per.ReadInteger16(r)
	userId += MCS_USERCHANNEL_BASE

	channelId, _ := per.ReadInteger16(r)
	per.ReadEnumerates(r)
	size, _ := per.ReadLength(r)
	// channel ID doesn't match a requested layer
	found := false
	channelName := ""
	for _, channel := range c.channels {
		if channel.ID == channelId {
			found = true
			channelName = channel.Name
			break
		}
	}
	if !found {
		if c.messageChannelId != 0 && channelId == c.messageChannelId {
			data, _ := core.ReadBytes(int(size), r)
			c.handleAutoDetect(data)
			return
		}
		slog.Error("mcs receive data for an unconnected layer")
		return
	}
	left, err := core.ReadBytes(int(size), r)
	if err != nil {
		c.Emit("error", errors.New(fmt.Sprintf("mcs recvData get data error %v", err)))
		return
	}
	slog.Debug("mcs emit sec", "channel", channelName, "left", core.Hex(left))
	c.Emit("sec", channelName, left)
}

func (c *MCSClient) recvChannelJoinConfirm(s []byte) {
	slog.Debug("recvChannelJoinConfirm", "s", core.Hex(s))
	r := bytes.NewReader(s)
	option, err := core.ReadUInt8(r)
	if err != nil {
		c.Emit("error", err)
		return
	}

	if !readMCSPDUHeader(option, CHANNEL_JOIN_CONFIRM) {
		c.Emit("error", errors.New("NODE_RDP_PROTOCOL_T125_MCS_WAIT_CHANNEL_JOIN_CONFIRM"))
		return
	}

	confirm, _ := per.ReadEnumerates(r)
	userId, _ := per.ReadInteger16(r)
	userId += MCS_USERCHANNEL_BASE

	if c.userId != userId {
		c.Emit("error", errors.New("NODE_RDP_PROTOCOL_T125_MCS_INVALID_USER_ID"))
		return
	}

	channelId, _ := per.ReadInteger16(r)
	if (confirm != 0) && (channelId == uint16(MCS_GLOBAL_CHANNEL_ID) || channelId == c.userId) {
		c.Emit("error", errors.New("NODE_RDP_PROTOCOL_T125_MCS_SERVER_MUST_CONFIRM_STATIC_CHANNEL"))
		return
	}
	if confirm == 0 {
		for i := 0; i < int(c.serverNetworkData.ChannelCount); i++ {
			if channelId == c.serverNetworkData.ChannelIdArray[i] {
				var t MCSChannelInfo
				t.ID = channelId
				t.Name = string(c.clientNetworkData.ChannelDefArray[i].Name[:])
				c.channels = append(c.channels, t)
			}
		}
	}
	c.channelsConnected++
	c.connectChannels()
}

// Connect-time auto-detection constants (MS-RDPBCGR 2.2.14).
const (
	secAutoDetectReq = uint16(0x1000)
	secAutoDetectRsp = uint16(0x2000)

	rdpRttRequestConnecttime = uint16(0x1001)
	rdpBwStartConnecttime    = uint16(0x1014)
	rdpBwPayload             = uint16(0x0002)
	rdpBwStopConnecttime     = uint16(0x002B)
	rdpRttRequest            = uint16(0x0001) // continuous RTT request
	rdpBwStart               = uint16(0x0014) // continuous BW start (no response)
	rdpBwStop                = uint16(0x0429) // continuous BW stop

	typeIDAutodetectResponse = uint8(0x01)
	rdpRttResponseType       = uint16(0x0000)
	rdpBwResultsConnecttime  = uint16(0x0003)
	rdpBwResults             = uint16(0x000B) // continuous BW results
)

// handleAutoDetect processes a connect-time auto-detect request from the server
// on the message channel. It responds to RTT and BW measurement requests so
// that gnome-remote-desktop proceeds to open the audio DVC channels.
func (c *MCSClient) handleAutoDetect(data []byte) {
	r := bytes.NewReader(data)
	secFlag, _ := core.ReadUint16LE(r)
	core.ReadUint16LE(r) // secFlagHi

	if secFlag&secAutoDetectReq == 0 {
		return
	}

	headerLength, _ := core.ReadUInt8(r)
	core.ReadUInt8(r) // headerTypeId
	seqNum, _ := core.ReadUint16LE(r)
	reqType, _ := core.ReadUint16LE(r)

	slog.Debug("AutoDetect request", "requestType", fmt.Sprintf("0x%04x", reqType),
		"sequenceNumber", seqNum, "headerLength", headerLength)

	switch reqType {
	case rdpRttRequestConnecttime, rdpRttRequest:
		c.sendAutoDetectResponse(seqNum, rdpRttResponseType, 0)
	case rdpBwStartConnecttime, rdpBwStart:
		c.bwStartTime = time.Now()
	case rdpBwStopConnecttime:
		elapsed := uint32(time.Since(c.bwStartTime).Milliseconds())
		if elapsed == 0 {
			elapsed = 1
		}
		c.sendAutoDetectResponse(seqNum, rdpBwResultsConnecttime, elapsed)
	case rdpBwStop:
		elapsed := uint32(time.Since(c.bwStartTime).Milliseconds())
		if elapsed == 0 {
			elapsed = 1
		}
		c.sendAutoDetectResponse(seqNum, rdpBwResults, elapsed)
	// rdpBwPayload requires no response
	}
}

// sendAutoDetectResponse sends an auto-detect response on the message channel.
// timeDelta is 0 for RTT responses (no BW fields); non-zero for BW responses
// (timeDelta in milliseconds since the corresponding BW_START was received).
func (c *MCSClient) sendAutoDetectResponse(sequenceNumber uint16, responseType uint16, timeDelta uint32) {
	includeBW := responseType == rdpBwResultsConnecttime || responseType == rdpBwResults
	headerLength := uint8(6)
	if includeBW {
		headerLength = 14
	}

	payload := &bytes.Buffer{}
	core.WriteUInt16LE(secAutoDetectRsp, payload)
	core.WriteUInt16LE(0, payload)
	core.WriteUInt8(headerLength, payload)
	core.WriteUInt8(typeIDAutodetectResponse, payload)
	core.WriteUInt16LE(sequenceNumber, payload)
	core.WriteUInt16LE(responseType, payload)
	if includeBW {
		core.WriteUInt32LE(timeDelta, payload) // timeDelta in milliseconds
		core.WriteUInt32LE(0, payload)         // byteCount (no BW_PAYLOAD was sent)
	}

	slog.Debug("AutoDetect response", "responseType", fmt.Sprintf("0x%04x", responseType),
		"sequenceNumber", sequenceNumber, "timeDelta", timeDelta)
	c.transport.Write(c.Pack(payload.Bytes(), c.messageChannelId))
}

func (c *MCSClient) Pack(data []byte, channelId uint16) []byte {
	buff := &bytes.Buffer{}
	writeMCSPDUHeader(c.sendOpCode, 0, buff)
	per.WriteInteger16(c.userId-MCS_USERCHANNEL_BASE, buff)
	per.WriteInteger16(channelId, buff)
	core.WriteUInt8(0x70, buff)
	per.WriteLength(len(data), buff)
	core.WriteBytes(data, buff)
	return buff.Bytes()
}

func (c *MCSClient) Write(data []byte) (n int, err error) {
	data = c.Pack(data, c.channels[0].ID)
	return c.transport.Write(data)
}

func (c *MCSClient) SendToChannel(channel string, data []byte) (n int, err error) {
	channelId := c.channels[0].ID
	for _, ch := range c.channels {
		if channel == ch.Name {
			channelId = ch.ID
			break
		}
	}

	data = c.Pack(data, channelId)
	return c.transport.Write(data)
}
