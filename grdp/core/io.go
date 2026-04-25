package core

import (
	"encoding/binary"
	"io"
)

type ReadBytesComplete func(result []byte, err error)

func StartReadBytes(len int, r io.Reader, cb ReadBytesComplete) {
	b := make([]byte, len)
	go func() {
		_, err := io.ReadFull(r, b)
		cb(b, err)
	}()
}

func ReadBytes(len int, r io.Reader) ([]byte, error) {
	b := make([]byte, len)
	length, err := io.ReadFull(r, b)
	return b[:length], err
}

func ReadByte(r io.Reader) (byte, error) {
	var buf [1]byte
	_, err := io.ReadFull(r, buf[:])
	return buf[0], err
}

func ReadUInt8(r io.Reader) (uint8, error) {
	var buf [1]byte
	_, err := io.ReadFull(r, buf[:])
	return buf[0], err
}

func ReadUint16LE(r io.Reader) (uint16, error) {
	var buf [2]byte
	_, err := io.ReadFull(r, buf[:])
	if err != nil {
		return 0, nil
	}
	return binary.LittleEndian.Uint16(buf[:]), nil
}

func ReadUint16BE(r io.Reader) (uint16, error) {
	var buf [2]byte
	_, err := io.ReadFull(r, buf[:])
	if err != nil {
		return 0, nil
	}
	return binary.BigEndian.Uint16(buf[:]), nil
}

func ReadUInt32LE(r io.Reader) (uint32, error) {
	var buf [4]byte
	_, err := io.ReadFull(r, buf[:])
	if err != nil {
		return 0, nil
	}
	return binary.LittleEndian.Uint32(buf[:]), nil
}

func ReadUInt32BE(r io.Reader) (uint32, error) {
	var buf [4]byte
	_, err := io.ReadFull(r, buf[:])
	if err != nil {
		return 0, nil
	}
	return binary.BigEndian.Uint32(buf[:]), nil
}

func WriteByte(data byte, w io.Writer) (int, error) {
	buf := [1]byte{data}
	return w.Write(buf[:])
}

func WriteBytes(data []byte, w io.Writer) (int, error) {
	return w.Write(data)
}

func WriteUInt8(data uint8, w io.Writer) (int, error) {
	buf := [1]byte{data}
	return w.Write(buf[:])
}

func WriteUInt16BE(data uint16, w io.Writer) (int, error) {
	var buf [2]byte
	binary.BigEndian.PutUint16(buf[:], data)
	return w.Write(buf[:])
}

func WriteUInt16LE(data uint16, w io.Writer) (int, error) {
	var buf [2]byte
	binary.LittleEndian.PutUint16(buf[:], data)
	return w.Write(buf[:])
}

func WriteUInt32LE(data uint32, w io.Writer) (int, error) {
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], data)
	return w.Write(buf[:])
}

func WriteUInt32BE(data uint32, w io.Writer) (int, error) {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], data)
	return w.Write(buf[:])
}

func PutUint16BE(data uint16) (uint8, uint8) {
	return uint8(data >> 8), uint8(data)
}

func Uint16BE(d0, d1 uint8) uint16 {
	return uint16(d0)<<8 | uint16(d1)
}

func RGB565ToRGB(data uint16) (r, g, b uint8) {
	r = uint8((data & 0xF800) >> 8)
	g = uint8((data & 0x07E0) >> 3)
	b = uint8((data & 0x001F) << 3)

	return
}
func RGB555ToRGB(data uint16) (r, g, b uint8) {
	r = uint8((data & 0x7C00) >> 7)
	g = uint8((data & 0x03E0) >> 2)
	b = uint8((data & 0x001F) << 3)

	return
}
