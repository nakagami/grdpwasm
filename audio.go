//go:build js && wasm

package main

import "syscall/js"

// playAudio forwards raw PCM bytes to the JavaScript audio scheduler.
// All conversion and scheduling is handled natively in JS for performance.
func playAudio(sampleRate, channels, bitsPerSample int, data []byte) {
	jsArr := js.Global().Get("Uint8Array").New(len(data))
	js.CopyBytesToJS(jsArr, data)
	js.Global().Call("rdpAudioPlay", sampleRate, channels, bitsPerSample, jsArr)
}
