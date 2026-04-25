package rdpgfx

// AVC420 / AVC444 bitmap stream parsing (MS-RDPEGFX 2.2.4.6 / 2.2.4.7).

import (
	"encoding/binary"
	"fmt"
	"log/slog"
	"time"
)

type avcRect struct {
	left, top, right, bottom uint16
}

type avcQuantQuality struct {
	qp          uint8
	quality     uint8
	progressive bool
}

type avc420Stream struct {
	regions      []avcRect
	quantQuality []avcQuantQuality
	h264Data     []byte
}

// parseAVC420Stream parses RDPGFX_AVC420_BITMAP_STREAM.
func parseAVC420Stream(data []byte) (*avc420Stream, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("avc420 stream too short (%d bytes)", len(data))
	}

	numRegions := binary.LittleEndian.Uint32(data[:4])
	if numRegions > 65536 {
		return nil, fmt.Errorf("avc420: too many regions: %d", numRegions)
	}

	// 4 bytes header + 8 bytes per region rect + 2 bytes per quant/quality
	metaSize := 4 + int(numRegions)*10
	if metaSize > len(data) {
		return nil, fmt.Errorf("avc420: metadata truncated (need %d, have %d)", metaSize, len(data))
	}

	off := 4
	regions := make([]avcRect, numRegions)
	for i := uint32(0); i < numRegions; i++ {
		regions[i] = avcRect{
			left:   binary.LittleEndian.Uint16(data[off:]),
			top:    binary.LittleEndian.Uint16(data[off+2:]),
			right:  binary.LittleEndian.Uint16(data[off+4:]),
			bottom: binary.LittleEndian.Uint16(data[off+6:]),
		}
		off += 8
	}

	qq := make([]avcQuantQuality, numRegions)
	for i := uint32(0); i < numRegions; i++ {
		qp := data[off]
		qual := data[off+1]
		qq[i] = avcQuantQuality{
			qp:          qp & 0x3F,
			quality:     qual,
			progressive: (qp & 0x40) != 0,
		}
		off += 2
	}

	return &avc420Stream{
		regions:      regions,
		quantQuality: qq,
		h264Data:     data[metaSize:],
	}, nil
}

// parseAVC444Stream parses RDPGFX_AVC444_BITMAP_STREAM.
// Returns the main AVC420 stream and the LC (luma-chroma) field.
//
//	LC=0: both streams present; we decode the main (YUV420) stream.
//	LC=1: main stream only.
//	LC=2: auxiliary only (chroma upgrade); returns nil stream.
func parseAVC444Stream(data []byte) (*avc420Stream, uint8, error) {
	if len(data) < 4 {
		return nil, 0, fmt.Errorf("avc444 stream too short")
	}

	cbField := binary.LittleEndian.Uint32(data[:4])
	lc := uint8((cbField >> 30) & 0x03)
	cbStream1 := int(cbField & 0x3FFFFFFF)
	rest := data[4:]

	switch lc {
	case 0: // Both streams; decode main
		if cbStream1 > len(rest) {
			return nil, lc, fmt.Errorf("avc444: stream1 size %d exceeds data %d", cbStream1, len(rest))
		}
		s, err := parseAVC420Stream(rest[:cbStream1])
		return s, lc, err
	case 1: // Main stream only
		streamData := rest
		if cbStream1 > 0 && cbStream1 <= len(rest) {
			streamData = rest[:cbStream1]
		}
		s, err := parseAVC420Stream(streamData)
		return s, lc, err
	case 2: // Auxiliary only (chroma upgrade) — not yet supported
		return nil, lc, nil
	default:
		return nil, lc, fmt.Errorf("avc444: invalid LC=%d", lc)
	}
}

// isH264Keyframe returns true when data contains an IDR NAL unit (type 5),
// which marks the start of a new GOP (key frame).  The scan handles both
// 3-byte (00 00 01) and 4-byte (00 00 00 01) Annex-B start codes.
func isH264Keyframe(data []byte) bool {
	for i := 0; i+4 <= len(data); i++ {
		// Look for Annex-B start code: 00 00 01 or 00 00 00 01.
		if data[i] == 0x00 && data[i+1] == 0x00 {
			var nalByte byte
			if data[i+2] == 0x01 && i+3 < len(data) {
				nalByte = data[i+3]
				i += 2
			} else if data[i+2] == 0x00 && i+3 < len(data) && data[i+3] == 0x01 && i+4 < len(data) {
				nalByte = data[i+4]
				i += 3
			} else {
				continue
			}
			nalType := nalByte & 0x1F
			if nalType == 5 { // IDR slice
				return true
			}
		}
	}
	return false
}

// decodeAVC420 decodes AVC420 bitmap data to BGRA pixels and returns the
// decoded frame plus the dirty rectangle list reported in the AVC420 stream
// header (in decoded-frame coordinates).  When regions is non-empty callers
// can blit only those regions instead of the whole frame, which dramatically
// reduces per-frame copying for typical desktop video where most of the
// frame is unchanged from the previous frame.
// The pooled return value is true when the returned slice was acquired from
// bitmapBufPool; the caller must then call releaseBitmapBuf on it.
func (g *GfxHandler) decodeAVC420(data []byte, destX, destY, destW, destH int) ([]byte, []avcRect, bool) {
	if g.h264dec == nil {
		if g.onH264Raw != nil {
			stream, err := parseAVC420Stream(data)
			if err == nil && len(stream.h264Data) > 0 {
				nalData := make([]byte, len(stream.h264Data))
				copy(nalData, stream.h264Data)
				g.onH264Raw(destX, destY, destW, destH, isH264Keyframe(nalData), nalData)
			}
		}
		return nil, nil, false
	}
	stream, err := parseAVC420Stream(data)
	if err != nil {
		slog.Warn("RDPGFX: AVC420 parse error", "err", err)
		return nil, nil, false
	}
	if len(stream.h264Data) == 0 {
		return nil, nil, false
	}
	frame, err := g.h264dec.Decode(stream.h264Data)
	if err != nil {
		slog.Warn("RDPGFX: H.264 decode error", "err", err)
		return nil, nil, false
	}
	if frame == nil {
		g.maybeRequestKeyframe()
		g.maybeNotifyDecoderBroken()
		slog.Debug("RDPGFX: H.264 decode returned nil frame (buffering?)")
		return nil, nil, false
	}
	g.keyframeRequested = false
	g.keyframeAttempts = 0
	slog.Debug("RDPGFX: AVC420 decoded", "frameW", frame.Width, "frameH", frame.Height, "destW", destW, "destH", destH, "regions", len(stream.regions), "h264Len", len(stream.h264Data))
	decoded, pooled := cropBGRA(frame.Data, frame.Width, frame.Height, destW, destH)
	return decoded, stream.regions, pooled
}

// decodeAVC444 decodes AVC444 bitmap data to BGRA pixels.
// Currently decodes the main YUV420 stream only (LC=0,1).
// The pooled return value is true when the returned slice was acquired from
// bitmapBufPool; the caller must then call releaseBitmapBuf on it.
func (g *GfxHandler) decodeAVC444(data []byte, destX, destY, destW, destH int) ([]byte, []avcRect, bool) {
	if g.h264dec == nil {
		if g.onH264Raw != nil {
			stream, _, err := parseAVC444Stream(data)
			if err == nil && stream != nil && len(stream.h264Data) > 0 {
				nalData := make([]byte, len(stream.h264Data))
				copy(nalData, stream.h264Data)
				g.onH264Raw(destX, destY, destW, destH, isH264Keyframe(nalData), nalData)
			}
		}
		return nil, nil, false
	}
	stream, lc, err := parseAVC444Stream(data)
	if err != nil {
		slog.Warn("RDPGFX: AVC444 parse error", "err", err)
		return nil, nil, false
	}
	if stream == nil {
		if lc == 2 {
			slog.Debug("RDPGFX: AVC444 LC=2 (chroma upgrade) skipped")
		}
		return nil, nil, false
	}
	if len(stream.h264Data) == 0 {
		return nil, nil, false
	}
	frame, err := g.h264dec.Decode(stream.h264Data)
	if err != nil {
		slog.Warn("RDPGFX: H.264 decode error (AVC444)", "err", err)
		return nil, nil, false
	}
	if frame == nil {
		g.maybeRequestKeyframe()
		g.maybeNotifyDecoderBroken()
		return nil, nil, false
	}
	g.keyframeRequested = false
	g.keyframeAttempts = 0
	slog.Debug("RDPGFX: AVC444 decoded", "frameW", frame.Width, "frameH", frame.Height,
		"destW", destW, "destH", destH, "h264Len", len(stream.h264Data))
	decoded, pooled := cropBGRA(frame.Data, frame.Width, frame.Height, destW, destH)
	return decoded, stream.regions, pooled
}

// maybeNotifyDecoderBroken calls onDecoderBroken (if set) once when the H.264
// decoder reports it is permanently unrecoverable.  The callback is fired at
// most once per GfxHandler so the caller can initiate a reconnect without
// repeated calls.
func (g *GfxHandler) maybeNotifyDecoderBroken() {
	if g.onDecoderBroken == nil {
		return
	}
	if g.decoderBrokenNotified {
		return
	}
	if g.h264dec == nil || !g.h264dec.IsBroken() {
		return
	}
	g.decoderBrokenNotified = true
	go g.onDecoderBroken()
}

// maybeRequestKeyframe calls onKeyframeNeeded (if set) the first time the
// H.264 decoder reports it is waiting for an IDR after a reset, and again
// every 3 seconds while still waiting.  This handles the case where the first
// refresh request goes unanswered (e.g. after an HW→SW decoder switch where
// the server never retransmits an IDR in time).
func (g *GfxHandler) maybeRequestKeyframe() {
	if g.onKeyframeNeeded == nil {
		return
	}
	if !g.h264dec.NeedsKeyframe() {
		return
	}
	// If the decoder just performed a hard reset, clear the rate-limit so we
	// send a fresh SendRefreshRect immediately rather than waiting up to 3 s.
	// Without this, the stall-nudge's lastKeyframeRequest timestamp blocks the
	// post-reset request, and the server never learns it must send a new IDR.
	if n := g.h264dec.HardResetCount(); n != g.lastHardResetCount {
		g.lastHardResetCount = n
		g.keyframeRequested = false
		g.keyframeAttempts = 0
		g.lastKeyframeRequest = time.Time{}
	}
	// Rate-limit: maybeRequestKeyframe is only called when the decoder is
	// already in a recovery state (NeedsKeyframe() == true, gated above).
	// Nudge the server every 1 s while we wait, instead of every 3 s, so
	// IDRs arrive sooner and the deferred-reset / post-reset wait windows
	// (~10 s of packets) get more chances to catch one before we are
	// forced to mark the decoder broken and reconnect.
	if g.keyframeRequested && time.Since(g.lastKeyframeRequest) < time.Second {
		return
	}
	g.keyframeRequested = true
	g.lastKeyframeRequest = time.Now()
	g.keyframeAttempts++
	// First two nudges use the lightweight RefreshRect.  If the server
	// keeps ignoring them (typical Windows behaviour during active video
	// playback), escalate to the full SuppressOutput off→on toggle which
	// forces a complete display repaint and a guaranteed new IDR.
	force := g.keyframeAttempts >= 3
	go g.onKeyframeNeeded(force)
}

// cropBGRA crops or pads BGRA pixel data to the target dimensions.
// When srcW == dstW and srcH == dstH the input slice is returned unchanged
// and pooled is false.  Otherwise a new buffer is acquired from bitmapBufPool
// (pooled == true) and the caller must call releaseBitmapBuf on it.
func cropBGRA(src []byte, srcW, srcH, dstW, dstH int) ([]byte, bool) {
	if srcW == dstW && srcH == dstH {
		return src, false
	}
	out := acquireBitmapBuf(dstW * dstH * 4)
	copyW := dstW
	if srcW < copyW {
		copyW = srcW
	}
	copyH := dstH
	if srcH < copyH {
		copyH = srcH
	}
	srcStride := srcW * 4
	dstStride := dstW * 4
	rowBytes := copyW * 4
	for y := 0; y < copyH; y++ {
		copy(out[y*dstStride:y*dstStride+rowBytes], src[y*srcStride:y*srcStride+rowBytes])
	}
	return out, true
}

// avcRegionUseThresholdPercent is the upper bound on the *fraction* of the
// decoded frame area that the union of dirty rects can cover before we give
// up and just blit the whole frame.  When the dirty area approaches the
// total area, the per-rect bookkeeping (allocation per rect, separate
// BitmapUpdate per rect) costs more than the bytes-copied savings.
const avcRegionUseThresholdPercent = 60

// shouldUseAVCRegions returns true when the per-region partial blit path is
// expected to be cheaper than a single full-frame blit.  A single region
// covering everything is treated as "no win"; many tiny regions covering
// most of the frame are similarly bypassed.
func shouldUseAVCRegions(regions []avcRect, frameW, frameH int) bool {
	if frameW <= 0 || frameH <= 0 {
		return false
	}
	total := frameW * frameH
	if total == 0 {
		return false
	}
	// Sum (with overlap double-counting) — overlap is uncommon in practice
	// and the threshold leaves slack for it.
	sum := 0
	for _, r := range regions {
		if r.right <= r.left || r.bottom <= r.top {
			continue
		}
		w := int(r.right - r.left)
		h := int(r.bottom - r.top)
		sum += w * h
		if sum*100 >= total*avcRegionUseThresholdPercent {
			return false
		}
	}
	return sum > 0
}

// blitAndEmitAVCRegions copies only the dirty rectangles of a decoded AVC
// frame into the persistent surface and emits a BitmapUpdate per region.
// All region coordinates are in decoded-frame space (i.e. relative to
// (left, top) on the surface).
//
// The emitted Data buffers are borrowed from bitmapBufPool and are returned
// to the pool once the synchronous onBitmap callback completes — see the
// BitmapUpdate lifecycle note.
func (g *GfxHandler) blitAndEmitAVCRegions(s *surface, left, top, frameW, frameH int, decoded []byte, regions []avcRect) {
	frameStride := frameW * 4
	surfStride := int(s.width) * 4
	updates := make([]BitmapUpdate, 0, len(regions))
	for _, rc := range regions {
		if rc.right <= rc.left || rc.bottom <= rc.top {
			continue
		}
		rx, ry := int(rc.left), int(rc.top)
		rw, rh := int(rc.right-rc.left), int(rc.bottom-rc.top)
		if rx+rw > frameW {
			rw = frameW - rx
		}
		if ry+rh > frameH {
			rh = frameH - ry
		}
		if rw <= 0 || rh <= 0 {
			continue
		}
		rowBytes := rw * 4
		region := acquireBitmapBuf(rw * rh * 4)
		for row := 0; row < rh; row++ {
			srcOff := (ry+row)*frameStride + rx*4
			if srcOff+rowBytes > len(decoded) {
				break
			}
			copy(region[row*rowBytes:row*rowBytes+rowBytes],
				decoded[srcOff:srcOff+rowBytes])

			// Mirror the same row into the persistent surface so any
			// subsequent codec (RFX progressive etc.) operating on the
			// same surface starts from the up-to-date pixels.
			dy := top + ry + row
			if dy < 0 || dy >= int(s.height) {
				continue
			}
			dstOff := dy*surfStride + (left+rx)*4
			if dstOff < 0 || dstOff+rowBytes > len(s.data) {
				continue
			}
			copy(s.data[dstOff:dstOff+rowBytes],
				decoded[srcOff:srcOff+rowBytes])
		}
		if !s.mapped || g.onBitmap == nil {
			releaseBitmapBuf(region)
			continue
		}
		destL := int(s.outputX) + left + rx
		destT := int(s.outputY) + top + ry
		updates = append(updates, BitmapUpdate{
			DestLeft: destL, DestTop: destT,
			DestRight: destL + rw - 1, DestBottom: destT + rh - 1,
			Width: rw, Height: rh, Bpp: 4, Data: region,
		})
	}
	g.emitAndReleaseUpdates(updates)
}
