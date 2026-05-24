//go:build js && wasm

package main

// keymap maps JS KeyboardEvent.code strings to RDP scan codes.
// Extended keys (E0 prefix) use the 0xE0xx convention from grdpsdl2/gxui.
var keymap = map[string]int{
	"Escape":        0x0001,
	"Digit1":        0x0002,
	"Digit2":        0x0003,
	"Digit3":        0x0004,
	"Digit4":        0x0005,
	"Digit5":        0x0006,
	"Digit6":        0x0007,
	"Digit7":        0x0008,
	"Digit8":        0x0009,
	"Digit9":        0x000A,
	"Digit0":        0x000B,
	"Minus":         0x000C,
	"Equal":         0x000D,
	"Backspace":     0x000E,
	"Tab":           0x000F,
	"KeyQ":          0x0010,
	"KeyW":          0x0011,
	"KeyE":          0x0012,
	"KeyR":          0x0013,
	"KeyT":          0x0014,
	"KeyY":          0x0015,
	"KeyU":          0x0016,
	"KeyI":          0x0017,
	"KeyO":          0x0018,
	"KeyP":          0x0019,
	"BracketLeft":   0x001A,
	"BracketRight":  0x001B,
	"Enter":         0x001C,
	"ControlLeft":   0x001D,
	"KeyA":          0x001E,
	"KeyS":          0x001F,
	"KeyD":          0x0020,
	"KeyF":          0x0021,
	"KeyG":          0x0022,
	"KeyH":          0x0023,
	"KeyJ":          0x0024,
	"KeyK":          0x0025,
	"KeyL":          0x0026,
	"Semicolon":     0x0027,
	"Quote":         0x0028,
	"Backquote":     0x0029,
	"ShiftLeft":     0x002A,
	"Backslash":     0x002B,
	"KeyZ":          0x002C,
	"KeyX":          0x002D,
	"KeyC":          0x002E,
	"KeyV":          0x002F,
	"KeyB":          0x0030,
	"KeyN":          0x0031,
	"KeyM":          0x0032,
	"Comma":         0x0033,
	"Period":        0x0034,
	"Slash":         0x0035,
	"ShiftRight":    0x0036,
	"NumpadMultiply": 0x0037,
	"AltLeft":       0x0038,
	"Space":         0x0039,
	"CapsLock":      0x003A,
	"F1":            0x003B,
	"F2":            0x003C,
	"F3":            0x003D,
	"F4":            0x003E,
	"F5":            0x003F,
	"F6":            0x0040,
	"F7":            0x0041,
	"F8":            0x0042,
	"F9":            0x0043,
	"F10":           0x0044,
	"ScrollLock":    0x0046,
	"Numpad7":       0x0047,
	"Numpad8":       0x0048,
	"Numpad9":       0x0049,
	"NumpadSubtract": 0x004A,
	"Numpad4":       0x004B,
	"Numpad5":       0x004C,
	"Numpad6":       0x004D,
	"NumpadAdd":     0x004E,
	"Numpad1":       0x004F,
	"Numpad2":       0x0050,
	"Numpad3":       0x0051,
	"Numpad0":       0x0052,
	"NumpadDecimal": 0x0053,
	"F11":           0x0057,
	"F12":           0x0058,
	"NumpadEqual":   0x0059,
	"NumpadEnter":   0xE01C,
	"ControlRight":  0xE01D,
	"NumpadDivide":  0xE035,
	"PrintScreen":   0xE037,
	"AltRight":      0xE038,
	"NumLock":       0xE045,
	"Pause":         0xE046,
	"Home":          0xE047,
	"ArrowUp":       0xE048,
	"PageUp":        0xE049,
	"ArrowLeft":     0xE04B,
	"ArrowRight":    0xE04D,
	"End":           0xE04F,
	"ArrowDown":     0xE050,
	"PageDown":      0xE051,
	"Insert":        0xE052,
	"Delete":        0xE053,
	"ContextMenu":   0xE05D,
	"MetaLeft":      0xE05B,
	"MetaRight":     0xE05C,
	// Japanese keyboard extras
	"IntlRo":        0x0073,
	"KanaMode":      0x0070,
	"IntlYen":       0x007D,
	"Convert":       0x0079,
	"NonConvert":    0x007B,
}

func jsCodeToRDP(code string, swapAltMeta bool) int {
	if swapAltMeta {
		switch code {
		case "AltLeft":
			code = "MetaLeft"
		case "AltRight":
			code = "MetaRight"
		case "MetaLeft":
			code = "AltLeft"
		case "MetaRight":
			code = "AltRight"
		}
	}
	if v, ok := keymap[code]; ok {
		return v
	}
	return 0
}
