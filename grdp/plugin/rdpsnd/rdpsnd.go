// Package rdpsnd implements the RDPSND (Audio Output Virtual Channel Extension)
// protocol (MS-RDPEA) for server-to-client audio redirection.
//
// It can operate over either a static virtual channel ("rdpsnd") or
// a dynamic virtual channel (AUDIO_PLAYBACK_DVC / AUDIO_PLAYBACK_LOSSY_DVC).
package rdpsnd

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"log/slog"

	"github.com/nakagami/grdp/core"
	"github.com/nakagami/grdp/plugin"
)

const (
	ChannelName   = plugin.RDPSND_SVC_CHANNEL_NAME
	ChannelOption = plugin.CHANNEL_OPTION_INITIALIZED |
		plugin.CHANNEL_OPTION_ENCRYPT_RDP
)

// RDPSND PDU types (MS-RDPEA 2.2)
const (
	SNDC_CLOSE       = 0x01
	SNDC_WAVE        = 0x02
	SNDC_SETVOLUME   = 0x03
	SNDC_SETPITCH    = 0x04
	SNDC_WAVECONFIRM = 0x05
	SNDC_TRAINING    = 0x06
	SNDC_FORMATS     = 0x07
	SNDC_CRYPTKEY    = 0x08
	SNDC_WAVEENCRYPT = 0x09
	SNDC_UDPWAVE     = 0x0A
	SNDC_UDPWAVELAST = 0x0B
	SNDC_QUALITYMODE = 0x0C
	SNDC_WAVE2       = 0x0D
)

// RDPSND capabilities flags
const (
	TSSNDCAPS_ALIVE  = 0x00000001
	TSSNDCAPS_VOLUME = 0x00000002
	TSSNDCAPS_PITCH  = 0x00000004
)

// Quality mode values (MS-RDPEA 2.2.2.9)
const (
	DYNAMIC_QUALITY = 0x0000
	MEDIUM_QUALITY  = 0x0002
	HIGH_QUALITY    = 0x0001
)

// Audio format tags
const (
	WAVE_FORMAT_PCM   = 0x0001
	WAVE_FORMAT_ADPCM = 0x0002
	WAVE_FORMAT_ALAW  = 0x0006
	WAVE_FORMAT_MULAW = 0x0007
)

// RDPSND version
// gnome-remote-desktop (grd-rdp-dvc-audio-playback.c) requires
// clientVersion >= 8 (CHANNEL_VERSION_WIN_8). FreeRDP WIN_7=6, WIN_8=8.
const (
	RDPSND_VERSION_MAJOR = 0x08
)

// AudioFormat represents a WAVEFORMATEX structure.
type AudioFormat struct {
	Tag            uint16
	Channels       uint16
	SamplesPerSec  uint32
	AvgBytesPerSec uint32
	BlockAlign     uint16
	BitsPerSample  uint16
	ExtraData      []byte
}

func (f AudioFormat) String() string {
	names := map[uint16]string{
		WAVE_FORMAT_PCM:   "PCM",
		WAVE_FORMAT_ADPCM: "ADPCM",
		WAVE_FORMAT_ALAW:  "A-Law",
		WAVE_FORMAT_MULAW: "μ-Law",
	}
	name, ok := names[f.Tag]
	if !ok {
		name = fmt.Sprintf("0x%04x", f.Tag)
	}
	return fmt.Sprintf("%s %dHz %dch %dbit", name, f.SamplesPerSec, f.Channels, f.BitsPerSample)
}

func (f AudioFormat) IsPCM() bool {
	return f.Tag == WAVE_FORMAT_PCM
}

func (f AudioFormat) pack() []byte {
	b := &bytes.Buffer{}
	binary.Write(b, binary.LittleEndian, f.Tag)
	binary.Write(b, binary.LittleEndian, f.Channels)
	binary.Write(b, binary.LittleEndian, f.SamplesPerSec)
	binary.Write(b, binary.LittleEndian, f.AvgBytesPerSec)
	binary.Write(b, binary.LittleEndian, f.BlockAlign)
	binary.Write(b, binary.LittleEndian, f.BitsPerSample)
	binary.Write(b, binary.LittleEndian, uint16(len(f.ExtraData)))
	b.Write(f.ExtraData)
	return b.Bytes()
}

func unpackAudioFormat(data []byte, offset int) (AudioFormat, int) {
	if len(data)-offset < 18 {
		return AudioFormat{}, offset
	}
	f := AudioFormat{
		Tag:            binary.LittleEndian.Uint16(data[offset:]),
		Channels:       binary.LittleEndian.Uint16(data[offset+2:]),
		SamplesPerSec:  binary.LittleEndian.Uint32(data[offset+4:]),
		AvgBytesPerSec: binary.LittleEndian.Uint32(data[offset+8:]),
		BlockAlign:     binary.LittleEndian.Uint16(data[offset+12:]),
		BitsPerSample:  binary.LittleEndian.Uint16(data[offset+14:]),
	}
	cbSize := int(binary.LittleEndian.Uint16(data[offset+16:]))
	if offset+18+cbSize <= len(data) {
		f.ExtraData = make([]byte, cbSize)
		copy(f.ExtraData, data[offset+18:offset+18+cbSize])
	}
	return f, offset + 18 + cbSize
}

// Handler implements the RDPSND protocol over a static virtual channel.
// It also serves as the DVC audio handler via ProcessData.
type Handler struct {
	channelSender core.ChannelSender

	serverFormats      []AudioFormat
	clientFormatIndices []int
	activeFormatIndex  int

	// Wave state
	waveTimestamp uint16
	waveBlockNo   uint8
	pendingWave   []byte
	expectingWave bool

	// DVC send callback for the current message's channel
	dvcSendFunc func([]byte)

	// viaDvc tracks whether the current message arrived via DVC
	viaDvc bool

	// Application callback: called with the active AudioFormat and PCM data
	onAudio func(AudioFormat, []byte)

	// onAudioReset is called when the server closes the audio channel
	// (SNDC_CLOSE). The application should flush its audio playback buffer
	// so that stale audio from before a seek does not keep playing.
	onAudioReset func()
}

// NewHandler creates a new RDPSND handler.
// onAudio is called with the active AudioFormat and PCM audio data for each wave.
func NewHandler(onAudio func(AudioFormat, []byte)) *Handler {
	return &Handler{
		activeFormatIndex: -1,
		onAudio:           onAudio,
	}
}

// SetAudioResetCallback sets a function that is called when the server
// closes the audio channel (e.g. on media seek). The application should
// flush its audio playback buffer in this callback.
func (h *Handler) SetAudioResetCallback(f func()) {
	h.onAudioReset = f
}

// --- plugin.ChannelTransport interface ---

func (h *Handler) GetType() (string, uint32) {
	return ChannelName, ChannelOption
}

func (h *Handler) Sender(s core.ChannelSender) {
	h.channelSender = s
}

// Process handles data from the static virtual channel (already reassembled).
func (h *Handler) Process(s []byte) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("rdpsnd: panic in Process", "err", r)
		}
	}()
	h.viaDvc = false
	h.ProcessData(s)
}

// ProcessData processes a reassembled RDPSND PDU payload.
// This is used by both the static VChannel path and the DVC path.
func (h *Handler) ProcessData(data []byte) {
	if h.expectingWave {
		h.processWaveBody(data)
		return
	}

	if len(data) < 4 {
		return
	}

	msgType := data[0]
	// data[1] is bPad
	bodySize := int(binary.LittleEndian.Uint16(data[2:4]))
	body := data[4:]
	if bodySize < len(body) {
		body = body[:bodySize]
	}

	slog.Debug("rdpsnd: recv", "msgType", fmt.Sprintf("0x%02x", msgType), "bodySize", bodySize)

	switch msgType {
	case SNDC_FORMATS:
		h.processServerFormats(body)
	case SNDC_TRAINING:
		h.processTraining(body)
	case SNDC_WAVE:
		h.processWaveInfo(body)
	case SNDC_WAVE2:
		h.processWave2(body)
	case SNDC_CLOSE:
		slog.Debug("rdpsnd: server closed audio channel")
		if h.onAudioReset != nil {
			h.onAudioReset()
		}
	case SNDC_SETVOLUME, SNDC_QUALITYMODE:
		// ignored
	default:
		slog.Debug("rdpsnd: unknown msgType", "type", fmt.Sprintf("0x%02x", msgType))
	}
}

// --- Server Audio Formats and Version (MS-RDPEA 2.2.2.1) ---

func (h *Handler) processServerFormats(body []byte) {
	if len(body) < 20 {
		slog.Warn("rdpsnd: Server Formats PDU too short")
		return
	}

	dwFlags := binary.LittleEndian.Uint32(body[0:])
	_ = dwFlags
	wNumberOfFormats := binary.LittleEndian.Uint16(body[14:])
	wVersion := binary.LittleEndian.Uint16(body[17:])

	slog.Debug("rdpsnd: Server Formats", "version", wVersion, "numFormats", wNumberOfFormats)

	offset := 20
	h.serverFormats = nil
	for i := 0; i < int(wNumberOfFormats); i++ {
		fmt, newOffset := unpackAudioFormat(body, offset)
		if newOffset == offset {
			break
		}
		h.serverFormats = append(h.serverFormats, fmt)
		slog.Debug("rdpsnd: server format", "idx", i, "fmt", fmt)
		offset = newOffset
	}

	// Select PCM formats we can handle
	h.clientFormatIndices = nil
	for i, f := range h.serverFormats {
		if f.IsPCM() && (f.BitsPerSample == 8 || f.BitsPerSample == 16) && (f.Channels == 1 || f.Channels == 2) {
			h.clientFormatIndices = append(h.clientFormatIndices, i)
		}
	}

	if len(h.clientFormatIndices) == 0 {
		slog.Warn("rdpsnd: no supported PCM format found")
	}

	h.sendClientFormats(wVersion)
}

func (h *Handler) sendClientFormats(serverVersion uint16) {
	version := serverVersion
	if version > RDPSND_VERSION_MAJOR {
		version = RDPSND_VERSION_MAJOR
	}

	formatData := &bytes.Buffer{}
	for _, idx := range h.clientFormatIndices {
		formatData.Write(h.serverFormats[idx].pack())
	}

	// Header: dwFlags(4) + dwVolume(4) + dwPitch(4) + wDGramPort(2)
	//         + wNumberOfFormats(2) + cLastBlockConfirmed(1) + wVersion(2) + bPad(1)
	hdr := &bytes.Buffer{}
	binary.Write(hdr, binary.LittleEndian, uint32(TSSNDCAPS_ALIVE)) // dwFlags
	binary.Write(hdr, binary.LittleEndian, uint32(0))               // dwVolume
	binary.Write(hdr, binary.LittleEndian, uint32(0))               // dwPitch
	binary.Write(hdr, binary.LittleEndian, uint16(0))               // wDGramPort
	binary.Write(hdr, binary.LittleEndian, uint16(len(h.clientFormatIndices)))
	hdr.WriteByte(0)                                                  // cLastBlockConfirmed
	binary.Write(hdr, binary.LittleEndian, version)                   // wVersion
	hdr.WriteByte(0)                                                  // bPad

	body := append(hdr.Bytes(), formatData.Bytes()...)

	pdu := &bytes.Buffer{}
	pdu.WriteByte(SNDC_FORMATS) // msgType
	pdu.WriteByte(0)            // bPad
	binary.Write(pdu, binary.LittleEndian, uint16(len(body)))
	pdu.Write(body)

	h.send(pdu.Bytes())
	slog.Debug("rdpsnd: sent Client Formats", "version", version, "numFormats", len(h.clientFormatIndices))

	// FreeRDP sends a Quality Mode PDU immediately after Client Formats.
	// Without it, Windows waits (up to ~10 seconds) before sending Training.
	h.sendQualityMode()
}

// --- Quality Mode (MS-RDPEA 2.2.2.9) ---

func (h *Handler) sendQualityMode() {
	pdu := &bytes.Buffer{}
	pdu.WriteByte(SNDC_QUALITYMODE)
	pdu.WriteByte(0) // bPad
	binary.Write(pdu, binary.LittleEndian, uint16(4))           // bodySize
	binary.Write(pdu, binary.LittleEndian, uint16(DYNAMIC_QUALITY)) // wQualityMode
	binary.Write(pdu, binary.LittleEndian, uint16(0))           // Reserved

	h.send(pdu.Bytes())
	slog.Debug("rdpsnd: sent QualityMode")
}

// --- Training (MS-RDPEA 2.2.2.3) ---

func (h *Handler) processTraining(body []byte) {
	if len(body) < 4 {
		return
	}
	wTimeStamp := binary.LittleEndian.Uint16(body[0:])
	wPackSize := binary.LittleEndian.Uint16(body[2:])
	slog.Debug("rdpsnd: Training", "timestamp", wTimeStamp, "packSize", wPackSize)

	pdu := &bytes.Buffer{}
	pdu.WriteByte(SNDC_TRAINING)
	pdu.WriteByte(0)
	binary.Write(pdu, binary.LittleEndian, uint16(4)) // bodySize
	binary.Write(pdu, binary.LittleEndian, wTimeStamp)
	binary.Write(pdu, binary.LittleEndian, wPackSize)

	h.send(pdu.Bytes())
	slog.Debug("rdpsnd: sent Training Confirm")
}

// --- Wave Info / Wave Data (MS-RDPEA 2.2.2.5 / 2.2.2.6) ---

func (h *Handler) processWaveInfo(body []byte) {
	if len(body) < 12 {
		slog.Warn("rdpsnd: WaveInfo body too short")
		return
	}

	wTimeStamp := binary.LittleEndian.Uint16(body[0:])
	wFormatNo := binary.LittleEndian.Uint16(body[2:])
	cBlockNo := body[4]
	initialData := make([]byte, 4)
	copy(initialData, body[8:12])

	h.waveTimestamp = wTimeStamp
	h.waveBlockNo = cBlockNo
	h.pendingWave = initialData

	if int(wFormatNo) < len(h.clientFormatIndices) {
		serverIdx := h.clientFormatIndices[wFormatNo]
		h.activeFormatIndex = serverIdx
	} else {
		slog.Warn("rdpsnd: WaveInfo format index out of range", "idx", wFormatNo, "max", len(h.clientFormatIndices))
	}

	h.expectingWave = true
	slog.Debug("rdpsnd: WaveInfo", "ts", wTimeStamp, "fmt", wFormatNo, "block", cBlockNo)
}

func (h *Handler) processWaveBody(data []byte) {
	h.expectingWave = false
	// First 4 bytes are padding (duplicate of WaveInfo header)
	var audioData []byte
	if len(data) > 4 {
		audioData = append(h.pendingWave, data[4:]...)
	} else {
		audioData = h.pendingWave
	}
	h.pendingWave = nil

	slog.Debug("rdpsnd: Wave data", "len", len(audioData))
	h.deliverAudio(audioData)
	h.sendWaveConfirm(h.waveTimestamp, h.waveBlockNo)
}

// --- Wave2 (MS-RDPEA 2.2.2.7) ---

func (h *Handler) processWave2(body []byte) {
	if len(body) < 12 {
		slog.Warn("rdpsnd: Wave2 body too short")
		return
	}

	wTimeStamp := binary.LittleEndian.Uint16(body[0:])
	wFormatNo := binary.LittleEndian.Uint16(body[2:])
	cBlockNo := body[4]
	audioData := body[12:]

	if int(wFormatNo) < len(h.clientFormatIndices) {
		serverIdx := h.clientFormatIndices[wFormatNo]
		h.activeFormatIndex = serverIdx
	} else {
		slog.Warn("rdpsnd: Wave2 format index out of range", "idx", wFormatNo, "max", len(h.clientFormatIndices))
	}

	slog.Debug("rdpsnd: Wave2", "ts", wTimeStamp, "fmt", wFormatNo, "block", cBlockNo, "dataLen", len(audioData))
	h.deliverAudio(audioData)
	h.sendWaveConfirm(wTimeStamp, cBlockNo)
}

// --- Wave Confirm (MS-RDPEA 2.2.2.8) ---

func (h *Handler) sendWaveConfirm(timestamp uint16, blockNo uint8) {
	pdu := &bytes.Buffer{}
	pdu.WriteByte(SNDC_WAVECONFIRM)
	pdu.WriteByte(0) // bPad
	binary.Write(pdu, binary.LittleEndian, uint16(4)) // bodySize
	binary.Write(pdu, binary.LittleEndian, timestamp)
	pdu.WriteByte(blockNo) // cConfBlockNo
	pdu.WriteByte(0)       // bPad

	h.send(pdu.Bytes())
	slog.Debug("rdpsnd: sent WaveConfirm", "ts", timestamp, "block", blockNo)
}

// --- Audio delivery ---

func (h *Handler) deliverAudio(data []byte) {
	if h.onAudio == nil || h.activeFormatIndex < 0 || h.activeFormatIndex >= len(h.serverFormats) {
		return
	}
	h.onAudio(h.serverFormats[h.activeFormatIndex], data)
}

// --- Send helpers ---

// send sends a response on the same path that the current message arrived on.
// Static channel messages get static channel responses; DVC messages get DVC responses.
func (h *Handler) send(data []byte) {
	if h.viaDvc && h.dvcSendFunc != nil {
		h.dvcSendFunc(data)
	} else if h.channelSender != nil {
		h.channelSender.SendToChannel(ChannelName, data)
	}
}

// --- DVC adapter ---

// DvcAdapter wraps an rdpsnd Handler to work as a DVC channel handler.
// Each DVC channel gets its own adapter so responses go to the correct channel.
type DvcAdapter struct {
	handler  *Handler
	sendFunc func([]byte)
}

// NewDvcAdapter creates a DVC adapter that routes audio data to the given Handler.
func NewDvcAdapter(handler *Handler) *DvcAdapter {
	return &DvcAdapter{handler: handler}
}

// Process implements drdynvc.DvcChannelHandler.
func (a *DvcAdapter) Process(data []byte) {
	a.handler.viaDvc = true
	a.handler.dvcSendFunc = a.sendFunc
	a.handler.ProcessData(data)
}

// SetSendFunc is called by the DVC client to provide the send function.
func (a *DvcAdapter) SetSendFunc(fn func([]byte)) {
	a.sendFunc = fn
}
