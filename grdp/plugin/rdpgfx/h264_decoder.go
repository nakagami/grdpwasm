package rdpgfx

// h264Frame holds a decoded H.264 frame in BGRA pixel format.
type h264Frame struct {
	Data          []byte // BGRA pixel data, 4 bytes per pixel
	Width, Height int
}

// h264Decoder decodes H.264 Annex B bitstream data into BGRA frames.
type h264Decoder interface {
	// Decode decodes H.264 NAL units and returns a decoded frame.
	// Returns nil frame (no error) when the decoder needs more input data.
	Decode(h264Data []byte) (*h264Frame, error)
	// NeedsKeyframe reports whether the decoder is waiting for a keyframe
	// (IDR) before it can produce output.  This happens after a decoder
	// reset; the caller should request a refresh from the server.
	NeedsKeyframe() bool
	// IsBroken reports whether the decoder is permanently unrecoverable.
	// When true, the application should reconnect the RDP session to
	// create a fresh decoder.
	IsBroken() bool
	// HardResetCount returns the number of hard resets performed so far.
	// Callers can use this to detect a new reset and clear rate-limit state.
	HardResetCount() int
	// Close releases all resources held by the decoder.
	Close()
}
