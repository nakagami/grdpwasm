// cliprdr_test.go
package cliprdr_test

import (
	"fmt"
	"testing"

	"github.com/nakagami/grdp/plugin/cliprdr"
)

func TestClipboardText(t *testing.T) {
	// Test text clipboard functionality
	testText := "Hello, Clipboard!"
	cliprdr.SetClipboardText(testText)
	
	result := cliprdr.GetClipboardText()
	if result != testText {
		t.Errorf("Expected %s, got %s", testText, result)
	}
	fmt.Printf("Text clipboard test passed: %s\n", result)
}

func TestFormatList(t *testing.T) {
	// Test format list (should contain only text format)
	formats := cliprdr.GetFormatList()
	if len(formats) == 0 {
		t.Error("Format list should not be empty")
	}
	fmt.Printf("Formats: %v\n", formats)
}

func TestOpenClipboard(t *testing.T) {
	// Test OpenClipboard (no-op in generic mode)
	ok := cliprdr.OpenClipboard(0)
	if !ok {
		t.Error("OpenClipboard should return true in generic mode")
	}
	ok = cliprdr.CloseClipboard()
	if !ok {
		t.Error("CloseClipboard should return true in generic mode")
	}
	fmt.Println("OpenClipboard test passed")
}

