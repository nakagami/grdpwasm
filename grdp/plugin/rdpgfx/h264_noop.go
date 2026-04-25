//go:build !h264

package rdpgfx

// newH264Decoder returns nil when built without the "h264" build tag.
// AVC codecs will be disabled in RDPGFX capability negotiation.
func newH264Decoder() h264Decoder {
	return nil
}
