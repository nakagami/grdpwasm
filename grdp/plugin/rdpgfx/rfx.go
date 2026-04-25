package rdpgfx

// Non-progressive RemoteFX (RFX) codec decoder (MS-RDPRFX).
// Used for RDPGFX_CODECID_CAVIDEO (0x0003) in WIRE_TO_SURFACE_PDU_1.
//
// Block type codes (same numeric values as progressive, different semantics):
//   0xCCC0 WBT_SYNC
//   0xCCC1 WBT_CODEC_VERSIONS
//   0xCCC2 WBT_CHANNELS
//   0xCCC3 WBT_CONTEXT        (+ 2-byte codecId/channelId)
//   0xCCC4 WBT_FRAME_BEGIN    (+ 2-byte codecId/channelId)
//   0xCCC5 WBT_FRAME_END      (+ 2-byte codecId/channelId)
//   0xCCC6 WBT_REGION         (+ 2-byte codecId/channelId)
//   0xCCC7 WBT_EXTENSION      (+ 2-byte codecId/channelId, contains TILESET)
//
// Tile sub-blocks inside TILESET use CBT_TILE (0xCAC3) with standard 6-byte header.

import (
	"encoding/binary"
	"log/slog"
	"runtime"
	"sync"
)

const (
	wbtSync          = 0xCCC0
	wbtCodecVersions = 0xCCC1
	wbtChannels      = 0xCCC2
	wbtContext       = 0xCCC3
	wbtFrameBegin    = 0xCCC4
	wbtFrameEnd      = 0xCCC5
	wbtRegion        = 0xCCC6
	wbtExtension     = 0xCCC7

	cbtRegion  = 0xCAC1
	cbtTileset = 0xCAC2
	cbtTile    = 0xCAC3
)

type rfxDecoder struct{}

func newRfxDecoder() *rfxDecoder {
	return &rfxDecoder{}
}

// Decode processes non-progressive RFX data, rendering tiles onto the
// provided surface buffer at the given (left, top) offset.
// Returns the bounding rectangles of decoded regions in surface coordinates.
func (d *rfxDecoder) Decode(data []byte, left, top int, surfData []byte, width, height int) []rfxRect {
	var rects []rfxRect
	var quants []rfxQuant

	offset := 0
	for offset+6 <= len(data) {
		blockType := binary.LittleEndian.Uint16(data[offset:])
		blockLen := int(binary.LittleEndian.Uint32(data[offset+2:]))

		if blockLen < 6 || offset+blockLen > len(data) {
			break
		}

		// Determine content start: blocks 0xCCC3-0xCCC7 have 2 extra bytes
		// (codecId + channelId) per TS_RFX_CODEC_CHANNELT.
		headerLen := 6
		if blockType >= wbtContext && blockType <= wbtExtension {
			headerLen = 8
		}

		if blockLen < headerLen {
			break
		}
		content := data[offset+headerLen : offset+blockLen]

		switch blockType {
		case wbtSync, wbtCodecVersions, wbtChannels, wbtContext,
			wbtFrameBegin, wbtFrameEnd:
			// Infrastructure blocks — no action needed for decoding.
		case wbtRegion:
			rects = d.parseRegion(content, left, top)
		case wbtExtension:
			quants = d.decodeTileset(content, left, top, surfData, width, height)
		}

		offset += blockLen
	}

	// If no rects were parsed from REGION (e.g. numRects=0), generate one
	// covering the entire surface per MS-RDPRFX 2.2.2.3.3.
	if len(rects) == 0 && quants != nil {
		rects = []rfxRect{{x: left, y: top, w: width - left, h: height - top}}
	}

	return rects
}

// parseRegion extracts rectangles from a WBT_REGION block.
// left/top are the WTS1 destination offsets applied to produce surface coordinates.
func (d *rfxDecoder) parseRegion(data []byte, left, top int) []rfxRect {
	if len(data) < 7 {
		return nil
	}

	// regionFlags := data[0]
	numRects := binary.LittleEndian.Uint16(data[1:])

	if numRects == 0 {
		return nil
	}

	needed := 3 + int(numRects)*8 + 4
	if len(data) < needed {
		return nil
	}

	rects := make([]rfxRect, numRects)
	off := 3
	for i := uint16(0); i < numRects; i++ {
		rects[i] = rfxRect{
			x: left + int(binary.LittleEndian.Uint16(data[off:])),
			y: top + int(binary.LittleEndian.Uint16(data[off+2:])),
			w: int(binary.LittleEndian.Uint16(data[off+4:])),
			h: int(binary.LittleEndian.Uint16(data[off+6:])),
		}
		off += 8
	}

	// Validate regionType
	regionType := binary.LittleEndian.Uint16(data[off:])
	if regionType != cbtRegion {
		slog.Debug("RFX: unexpected regionType", "type", regionType)
	}

	return rects
}

// decodeTileset parses and decodes all tiles from a WBT_EXTENSION/TILESET block.
// Format: subtype(2) + idx(2) + properties(2) + numQuant(1) + tileSize(1) +
//
//	numTiles(2) + tilesDataSize(4) + quants(numQuant*5) + tiles
//
// Returns the quant table for caller reference.
func (d *rfxDecoder) decodeTileset(data []byte, left, top int, surfData []byte, width, height int) []rfxQuant {
	if len(data) < 14 {
		return nil
	}

	subtype := binary.LittleEndian.Uint16(data[0:])
	if subtype != cbtTileset {
		return nil
	}

	properties := binary.LittleEndian.Uint16(data[4:])
	numQuant := int(data[6])
	// tileSize := data[7]
	numTiles := int(binary.LittleEndian.Uint16(data[8:]))
	// tilesDataSize := binary.LittleEndian.Uint32(data[10:])

	// Extract RLGR entropy algorithm from TILESET properties.
	// TILESET properties bit layout (MS-RDPRFX / FreeRDP):
	//   bits 10-13: et (entropy type) - 0x01=RLGR1, 0x04=RLGR3
	rlgrMode := 1
	et := (properties >> 10) & 0x0F
	if et == 0x04 {
		rlgrMode = 3
	}

	off := 14

	// Parse quantization tables (5 bytes each, 10 nibbles)
	if off+numQuant*5 > len(data) {
		return nil
	}
	quants := make([]rfxQuant, numQuant)
	for i := 0; i < numQuant; i++ {
		quants[i] = parseRfxQuant(data[off:])
		off += 5
	}

	// Collect tile content slices for parallel decoding.
	type tileWork struct {
		content []byte
	}
	var tiles []tileWork
	for i := 0; i < numTiles; i++ {
		if off+6 > len(data) {
			break
		}
		tileBlockType := binary.LittleEndian.Uint16(data[off:])
		tileBlockLen := int(binary.LittleEndian.Uint32(data[off+2:]))

		if tileBlockType != cbtTile {
			break
		}
		if tileBlockLen < 19 || off+tileBlockLen > len(data) {
			break
		}

		tiles = append(tiles, tileWork{content: data[off+6 : off+tileBlockLen]})
		off += tileBlockLen
	}

	// Decode tiles concurrently — each tile writes to its own non-overlapping
	// 64×64 region of the output buffer so no locking is needed. For small
	// tile counts the goroutine + channel + WaitGroup overhead exceeds the
	// per-tile work, so fall back to serial decoding below the threshold.
	const parallelTileThreshold = 4
	if len(tiles) >= parallelTileThreshold {
		workers := runtime.NumCPU()
		if workers > len(tiles) {
			workers = len(tiles)
		}
		var wg sync.WaitGroup
		ch := make(chan tileWork, len(tiles))
		for _, t := range tiles {
			ch <- t
		}
		close(ch)
		wg.Add(workers)
		for w := 0; w < workers; w++ {
			go func() {
				defer wg.Done()
				defer func() {
					if r := recover(); r != nil {
						slog.Error("RFX: tile decode panic", "err", r)
					}
				}()
				for t := range ch {
					d.decodeTile(t.content, quants, rlgrMode, left, top, surfData, width, height)
				}
			}()
		}
		wg.Wait()
	} else {
		for _, t := range tiles {
			d.decodeTile(t.content, quants, rlgrMode, left, top, surfData, width, height)
		}
	}

	return quants
}

// decodeTile decodes a single non-progressive RFX tile.
// Format: quantIdxY(1) + quantIdxCb(1) + quantIdxCr(1) + xIdx(2) + yIdx(2) +
//
//	YLen(2) + CbLen(2) + CrLen(2) + YData(YLen) + CbData(CbLen) + CrData(CrLen)
func (d *rfxDecoder) decodeTile(data []byte, quants []rfxQuant, rlgrMode int, left, top int, output []byte, outW, outH int) {
	if len(data) < 13 {
		return
	}

	quantIdxY := int(data[0])
	quantIdxCb := int(data[1])
	quantIdxCr := int(data[2])
	xIdx := int(binary.LittleEndian.Uint16(data[3:]))
	yIdx := int(binary.LittleEndian.Uint16(data[5:]))
	yLen := int(binary.LittleEndian.Uint16(data[7:]))
	cbLen := int(binary.LittleEndian.Uint16(data[9:]))
	crLen := int(binary.LittleEndian.Uint16(data[11:]))

	off := 13
	yData := safeSlice(data, off, yLen)
	off += yLen
	cbData := safeSlice(data, off, cbLen)
	off += cbLen
	crData := safeSlice(data, off, crLen)

	qY := rfxGetQuant(quants, quantIdxY)
	qCb := rfxGetQuant(quants, quantIdxCb)
	qCr := rfxGetQuant(quants, quantIdxCr)

	yPixels := rfxDecodeComponent(yData, qY, rlgrMode)
	cbPixels := rfxDecodeComponent(cbData, qCb, rlgrMode)
	crPixels := rfxDecodeComponent(crData, qCr, rlgrMode)

	// Apply WTS1 left/top offset: tile pixel position on surface =
	// left + xIdx*64, top + yIdx*64 (per FreeRDP/MS-RDPRFX).
	rfxPlaceTileAbs(yPixels, cbPixels, crPixels, left+xIdx*rfxTileSize, top+yIdx*rfxTileSize, output, outW, outH)

	coeffPool.Put(yPixels)
	coeffPool.Put(cbPixels)
	coeffPool.Put(crPixels)
}

// DecodeSurfaceRFX decodes non-progressive RemoteFX (MS-RDPRFX) encoded data
// into a top-down BGRA pixel buffer suitable for surface bitmap commands.
func DecodeSurfaceRFX(data []byte, width, height int) []byte {
	output := make([]byte, width*height*4)
	dec := newRfxDecoder()
	dec.Decode(data, 0, 0, output, width, height)
	return output
}
