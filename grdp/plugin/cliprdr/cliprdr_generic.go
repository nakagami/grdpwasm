// Generic, OS-independent clipboard support with text format only
package cliprdr

import (
	"log/slog"
)

// SimpleTextClipboard provides OS-independent text-only clipboard support
type SimpleTextClipboard struct {
	textContent string
}

var textClipboard = &SimpleTextClipboard{}

// GetClipboardText returns the current text content
func GetClipboardText() string {
	return textClipboard.textContent
}

// SetClipboardText sets the clipboard text content
func SetClipboardText(text string) {
	textClipboard.textContent = text
	slog.Debug("Text clipboard set", "length", len(text))
}

// GetFormatList returns available formats (text only in generic mode)
func GetFormatList() []CliprdrFormat {
	formatId := uint32(CF_UNICODETEXT)
	formats := make([]CliprdrFormat, 0, 1)
	formats = append(formats, CliprdrFormat{
		FormatId:   formatId,
		FormatName: "CF_UNICODETEXT",
	})
	return formats
}

// ClipWatcher is a no-op in generic mode
func ClipWatcher(c *CliprdrClient) {
	slog.Info("Generic clipboard watcher (text-only mode) started")
	// In generic mode, we don't actively monitor system clipboard
	// Format list is provided statically
	select {} // Block indefinitely
}

// Stub functions for compatibility (no-op in generic mode)

func OpenClipboard(hwnd uintptr) bool {
	return true
}

func CloseClipboard() bool {
	return true
}

func CountClipboardFormats() int32 {
	return 1
}

func IsClipboardFormatAvailable(id uint32) bool {
	return id == CF_UNICODETEXT || id == CF_TEXT
}

func EnumClipboardFormats(formatId uint32) uint32 {
	if formatId == 0 {
		return CF_UNICODETEXT
	}
	return 0
}

func GetClipboardFormatName(id uint32) string {
	switch id {
	case CF_TEXT:
		return "CF_TEXT"
	case CF_UNICODETEXT:
		return "CF_UNICODETEXT"
	default:
		return ""
	}
}

func EmptyClipboard() bool {
	textClipboard.textContent = ""
	return true
}

func RegisterClipboardFormat(format string) uint32 {
	// Simple hash-based format ID generation
	sum := uint32(0)
	for _, c := range format {
		sum = sum*31 + uint32(c)
	}
	return sum | 0xC000 // Set bit 14 for custom formats
}

func IsClipboardOwner(h uintptr) bool {
	return false // Not applicable in generic mode
}

func HmemAlloc(data []byte) uintptr {
	// In generic mode, return a pointer to the data
	if len(data) == 0 {
		return 0
	}
	return uintptr(len(data))
}

func SetClipboardData(formatId uint32, hmem uintptr) bool {
	// No-op in generic mode
	return true
}

func GetClipboardData(formatId uint32) string {
	if formatId == CF_UNICODETEXT || formatId == CF_TEXT {
		return GetClipboardText()
	}
	return ""
}

func GlobalSize(hMem uintptr) uintptr {
	return hMem
}

func GlobalLock(hMem uintptr) uintptr {
	return hMem
}

func GlobalUnlock(hMem uintptr) {
	// No-op
}

func OleGetClipboard() *IDataObject {
	return nil
}

func OleSetClipboard(dataObject *IDataObject) bool {
	return true
}

func OleIsCurrentClipboard(dataObject *IDataObject) bool {
	return true
}

func GetFileNames() []string {
	// Text-only mode doesn't support file transfer
	return []string{}
}

func GetFileInfo(sys interface{}) (uint32, []byte, uint32, uint32) {
	// Text-only mode doesn't support file transfer
	return 0, []byte{}, 0, 0
}

// IDataObject stub - not used in text-only mode
type IDataObject struct {
	ptr uintptr
}

type IUnknown struct {
	ptr uintptr
}

type FORMATETC struct {
	CFormat        uint32
	DvTargetDevice uintptr
	Aspect         uint32
	Index          int32
	Tymed          uint32
}

type STGMEDIUM struct {
	Tymed          uint32
	UnionMember    uintptr
	PUnkForRelease *IUnknown
}

func (s *STGMEDIUM) Bytes() ([]byte, error) {
	return []byte{}, nil
}

func CreateDataObject(c *CliprdrClient) *IDataObject {
	return nil
}
