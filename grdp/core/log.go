package core

import (
	"encoding/hex"
	"log/slog"
)

// Hex wraps a byte slice as a slog.LogValuer that lazily encodes the bytes
// as a hexadecimal string only when the slog handler actually formats it.
//
// Use Hex(buf) instead of hex.EncodeToString(buf) inside slog.Debug calls on
// hot paths: when the logger's level filter discards the record (the common
// case in production), the encode and the per-call string allocation are
// skipped entirely.
type Hex []byte

// LogValue implements slog.LogValuer.
func (h Hex) LogValue() slog.Value {
	return slog.StringValue(hex.EncodeToString(h))
}
