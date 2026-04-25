package tpkt

import (
	"bytes"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nakagami/grdp/core"
	"github.com/nakagami/grdp/emission"
	"github.com/nakagami/grdp/protocol/nla"
)

var writePool = sync.Pool{
	New: func() interface{} { return make([]byte, 0, 4096) },
}

// take idea from https://github.com/Madnikulin50/gordp

/**
 * Type of tpkt packet
 * Fastpath is use to shortcut RDP stack
 * @see http://msdn.microsoft.com/en-us/library/cc240621.aspx
 * @see http://msdn.microsoft.com/en-us/library/cc240589.aspx
 */
const (
	FASTPATH_ACTION_FASTPATH = 0x0
	FASTPATH_ACTION_X224     = 0x3
)

/**
 * TPKT layer of rdp stack
 */
type TPKT struct {
	emission.Emitter
	Conn             *core.SocketLayer
	ntlm             *nla.NTLMv2
	secFlag          byte
	lastShortLength  int
	fastPathListener core.FastPathListener
	ntlmSec          *nla.NTLMv2Security
}

func New(s *core.SocketLayer, ntlm *nla.NTLMv2) *TPKT {
	t := &TPKT{
		Emitter: *emission.NewEmitter(),
		Conn:    s,
		secFlag: 0,
		ntlm:    ntlm}
	core.StartReadBytes(2, s, t.recvHeader)
	return t
}

func (t *TPKT) StartTLS() error {
	return t.Conn.StartTLS()
}

func (t *TPKT) StartNLA() error {
	// Set a deadline for the entire NLA handshake (TLS + NTLM auth)
	// to prevent hanging indefinitely when the server is slow to respond.
	t.Conn.SetDeadline(time.Now().Add(30 * time.Second))
	defer t.Conn.SetDeadline(time.Time{}) // clear deadline after NLA completes

	slog.Debug("StartNLA: TLS handshake begin")
	err := t.StartTLS()
	if err != nil {
		slog.Error("StartNLA", "start tls failed", err)
		return err
	}
	slog.Debug("StartNLA: TLS handshake complete")
	req := nla.EncodeDERTRequest([]nla.Message{t.ntlm.GetNegotiateMessage()}, nil, nil)
	slog.Debug("StartNLA send", "req", core.Hex(req), "len", len(req))
	_, err = t.Conn.Write(req)
	if err != nil {
		slog.Error("send NegotiateMessage", "err", err)
		return err
	}

	resp := make([]byte, 1024)
	n, err := t.Conn.Read(resp)
	slog.Debug("StartNLA recv", "n", n, "err", err)
	if err != nil {
		return fmt.Errorf("read %s", err)
	} else {
		slog.Debug("StartNLA Read success")
	}
	return t.recvChallenge(resp[:n])
}

func (t *TPKT) recvChallenge(data []byte) error {
	slog.Debug("recvChallenge", "data", core.Hex(data))
	tsreq, err := nla.DecodeDERTRequest(data)
	if err != nil {
		slog.Debug("DecodeDERTRequest", "err", err)
		return err
	}
	slog.Debug("recvChallenge", "tsreq", tsreq)
	// get pubkey
	pubkey, err := t.Conn.TlsPubKey()
	slog.Debug("recvChallenge", "pubkey", core.Hex(pubkey))

	authMsg, ntlmSec := t.ntlm.GetAuthenticateMessage(tsreq.NegoTokens[0].Data)
	t.ntlmSec = ntlmSec

	encryptPubkey := ntlmSec.GssEncrypt(pubkey)
	req := nla.EncodeDERTRequest([]nla.Message{authMsg}, nil, encryptPubkey)
	slog.Debug("recvChallenge", "send", core.Hex(req), "len", len(req))
	_, err = t.Conn.Write(req)
	if err != nil {
		slog.Error("send AuthenticateMessage", "err", err)
		return err
	}

	slog.Debug("recvChallenge read challenge start")
	resp := make([]byte, 1024)
	n, err := t.Conn.Read(resp)
	if err != nil {
		slog.Error("recvChallenge", "err", err)
		return fmt.Errorf("read %s", err)
	}

	return t.recvPubKeyInc(resp[:n])
}

func (t *TPKT) recvPubKeyInc(data []byte) error {
	slog.Debug("recvPubKeyInc", "data", core.Hex(data), "len", len(data))
	tsreq, err := nla.DecodeDERTRequest(data)
	if err != nil {
		slog.Debug("DecodeDERTRequest", "err", err)
		return err
	}
	slog.Debug("PubKeyAuth", "key", core.Hex(tsreq.PubKeyAuth))
	//ignore
	pubkey := t.ntlmSec.GssDecrypt([]byte(tsreq.PubKeyAuth))
	slog.Debug("GssDecrypy", "pubkey", core.Hex(pubkey))
	domain, username, password := t.ntlm.GetEncodedCredentials()
	credentials := nla.EncodeDERTCredentials(domain, username, password)
	authInfo := t.ntlmSec.GssEncrypt(credentials)
	req := nla.EncodeDERTRequest(nil, authInfo, nil)
	_, err = t.Conn.Write(req)
	if err != nil {
		slog.Debug("send AuthenticateMessage", "err", err)
		return err
	}

	return nil
}

func (t *TPKT) Read(b []byte) (n int, err error) {
	return t.Conn.Read(b)
}

func (t *TPKT) Write(data []byte) (n int, err error) {
	buf := writePool.Get().([]byte)
	size := uint16(len(data) + 4)
	buf = append(buf[:0], FASTPATH_ACTION_X224, 0, byte(size>>8), byte(size))
	buf = append(buf, data...)
	n, err = t.Conn.Write(buf)
	writePool.Put(buf[:0])
	return
}

func (t *TPKT) Close() error {
	return t.Conn.Close()
}

func (t *TPKT) SetFastPathListener(f core.FastPathListener) {
	t.fastPathListener = f
}

func (t *TPKT) SendFastPath(secFlag byte, data []byte) (n int, err error) {
	buf := writePool.Get().([]byte)
	hdr := uint16(len(data)+3) | 0x8000
	buf = append(buf[:0], FASTPATH_ACTION_FASTPATH|((secFlag&0x3)<<6), byte(hdr>>8), byte(hdr))
	buf = append(buf, data...)
	slog.Debug("TPTK SendFastPath", "buff", core.Hex(buf))
	n, err = t.Conn.Write(buf)
	writePool.Put(buf[:0])
	return
}

func (t *TPKT) recvHeader(s []byte, err error) {
	if err != nil {
		t.Emit("error", err)
		return
	}
	r := bytes.NewReader(s)
	version, _ := core.ReadUInt8(r)
	slog.Debug("TPKT recvHeader", "version", version, "raw", core.Hex(s))
	if version == FASTPATH_ACTION_X224 {
		core.StartReadBytes(2, t.Conn, t.recvExtendedHeader)
	} else {
		t.secFlag = (version >> 6) & 0x3
		length, _ := core.ReadUInt8(r)
		t.lastShortLength = int(length)
		slog.Debug("TPKT FastPath", "secFlag", t.secFlag, "length", t.lastShortLength)
		if t.lastShortLength&0x80 != 0 {
			core.StartReadBytes(1, t.Conn, t.recvExtendedFastPathHeader)
		} else {
			core.StartReadBytes(t.lastShortLength-2, t.Conn, t.recvFastPath)
		}
	}
}

func (t *TPKT) recvExtendedHeader(s []byte, err error) {
	if err != nil {
		return
	}
	r := bytes.NewReader(s)
	size, _ := core.ReadUint16BE(r)
	core.StartReadBytes(int(size-4), t.Conn, t.recvData)
}

func (t *TPKT) recvData(s []byte, err error) {
	if err != nil {
		return
	}
	t.Emit("data", s)
	core.StartReadBytes(2, t.Conn, t.recvHeader)
}

func (t *TPKT) recvExtendedFastPathHeader(s []byte, err error) {
	r := bytes.NewReader(s)
	rightPart, err := core.ReadUInt8(r)
	if err != nil {
		slog.Error("TPTK recvExtendedFastPathHeader", "err", err)
		return
	}

	leftPart := t.lastShortLength & ^0x80
	packetSize := (leftPart << 8) + int(rightPart)
	core.StartReadBytes(packetSize-3, t.Conn, t.recvFastPath)
}

func (t *TPKT) recvFastPath(s []byte, err error) {
	if err != nil {
		slog.Debug("TPKT recvFastPath error", "err", err)
		return
	}
	slog.Debug("TPKT recvFastPath", "len", len(s))
	t.fastPathListener.RecvFastPath(t.secFlag, s)
	core.StartReadBytes(2, t.Conn, t.recvHeader)
}
