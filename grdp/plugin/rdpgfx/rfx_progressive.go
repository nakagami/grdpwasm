package rdpgfx

// RFX Progressive Codec decoder (MS-RDPRFX / MS-RDPEGFX 2.2.4).
// Handles RDPGFX_CODECID_CAPROGRESSIVE (0x0009) in WIRE_TO_SURFACE_PDU_2.

import (
	"encoding/binary"
	"fmt"
	"log/slog"
)

// Progressive block types (different from non-progressive WBT_* at same values!)
const (
	progWBTSync        = 0xCCC0
	progWBTFrameBegin  = 0xCCC1
	progWBTFrameEnd    = 0xCCC2
	progWBTContext     = 0xCCC3
	progWBTRegion      = 0xCCC4
	progWBTTileSimple  = 0xCCC5
	progWBTTileFirst   = 0xCCC6
	progWBTTileUpgrade = 0xCCC7
)

const rfxTileSize = 64

// rfxQuant holds the 10 quantization values for one component (5 bytes, 10 nibbles).
type rfxQuant struct {
	LL3, LH3, HL3, HH3 uint8
	LH2, HL2, HH2      uint8
	LH1, HL1, HH1      uint8
}

type rfxProgressiveDecoder struct{}

func newRfxProgressiveDecoder() *rfxProgressiveDecoder {
	return &rfxProgressiveDecoder{}
}

// rfxRect represents a rectangle of decoded tiles.
type rfxRect struct {
	x, y, w, h int
}

// Decode processes RFX Progressive codec data, rendering tiles onto the
// provided surface buffer. Returns the bounding rectangles of decoded regions.
func (d *rfxProgressiveDecoder) Decode(data []byte, surfData []byte, width, height int) []rfxRect {
	var rects []rfxRect

	offset := 0
	for offset+6 <= len(data) {
		blockType := binary.LittleEndian.Uint16(data[offset:])
		blockLen := binary.LittleEndian.Uint32(data[offset+2:])

		if blockLen < 6 || offset+int(blockLen) > len(data) {
			break
		}

		blockData := data[offset+6 : offset+int(blockLen)]

		switch blockType {
		case progWBTSync, progWBTFrameBegin, progWBTFrameEnd, progWBTContext:
		// Infrastructure blocks — no action needed.
		case progWBTRegion:
			// Tiles are embedded inside the region block; parseRegion decodes them.
			regionRects, _ := d.parseRegion(blockData, surfData, width, height)
			rects = append(rects, regionRects...)
		default:
			slog.Debug(fmt.Sprintf("RFX: unknown progressive block 0x%04X", blockType))
		}

		offset += int(blockLen)
	}

	return rects
}

// parseRegion extracts rects and quant tables from a PROGRESSIVE_WBT_REGION block,
// and decodes the tile sub-blocks embedded within it onto the surface.
// Per MS-RDPEGFX 2.2.4, tile blocks (TILE_SIMPLE/TILE_FIRST) are sub-blocks
// inside the REGION block, not top-level stream blocks.
func (d *rfxProgressiveDecoder) parseRegion(data []byte, surfData []byte, outW, outH int) ([]rfxRect, []rfxQuant) {
	if len(data) < 12 {
		return nil, nil
	}

	// tileSize := data[0]
	numRects := binary.LittleEndian.Uint16(data[1:])
	numQuant := data[3]
	numProgQuant := data[4]
	// flags := data[5]
	// numTiles := binary.LittleEndian.Uint16(data[6:])
	// tileDataSize := binary.LittleEndian.Uint32(data[8:])

	offset := 12

	// Parse rects (8 bytes each: x, y, width, height as uint16)
	rects := make([]rfxRect, numRects)
	for i := uint16(0); i < numRects; i++ {
		if offset+8 > len(data) {
			return nil, nil
		}
		rx := int(binary.LittleEndian.Uint16(data[offset:]))
		ry := int(binary.LittleEndian.Uint16(data[offset+2:]))
		rw := int(binary.LittleEndian.Uint16(data[offset+4:]))
		rh := int(binary.LittleEndian.Uint16(data[offset+6:]))
		rects[i] = rfxRect{x: rx, y: ry, w: rw, h: rh}
		offset += 8
	}

	// Parse quant values (5 bytes each)
	quants := make([]rfxQuant, numQuant)
	for i := uint8(0); i < numQuant; i++ {
		if offset+5 > len(data) {
			return nil, nil
		}
		quants[i] = parseRfxQuant(data[offset:])
		offset += 5
	}

	// Skip progressive quant values (RFX_PROGRESSIVE_CODEC_QUANT, 16 bytes each)
	offset += int(numProgQuant) * 16

	// Decode tile sub-blocks embedded within this region block.
	for offset+6 <= len(data) {
		tileType := binary.LittleEndian.Uint16(data[offset:])
		tileLen := binary.LittleEndian.Uint32(data[offset+2:])
		if tileLen < 6 || offset+int(tileLen) > len(data) {
			break
		}
		tileData := data[offset+6 : offset+int(tileLen)]
		switch tileType {
		case progWBTTileSimple:
			d.decodeTileSimple(tileData, quants, surfData, outW, outH)
		case progWBTTileFirst:
			d.decodeTileFirst(tileData, quants, surfData, outW, outH)
		case progWBTTileUpgrade:
			// Progressive upgrade pass — first pass is sufficient for display.
		default:
			slog.Debug(fmt.Sprintf("RFX: unknown progressive tile type 0x%04X", tileType))
		}
		offset += int(tileLen)
	}

	return rects, quants
}

func parseRfxQuant(data []byte) rfxQuant {
	return rfxQuant{
		LL3: data[0] & 0x0F,
		LH3: data[0] >> 4,
		HL3: data[1] & 0x0F,
		HH3: data[1] >> 4,
		LH2: data[2] & 0x0F,
		HL2: data[2] >> 4,
		HH2: data[3] & 0x0F,
		LH1: data[3] >> 4,
		HL1: data[4] & 0x0F,
		HH1: data[4] >> 4,
	}
}

// decodeTileSimple handles PROGRESSIVE_WBT_TILE_SIMPLE (0xCCC5).
func (d *rfxProgressiveDecoder) decodeTileSimple(data []byte, quants []rfxQuant, output []byte, outW, outH int) {
	if len(data) < 16 {
		return
	}

	quantIdxY := data[0]
	quantIdxCb := data[1]
	quantIdxCr := data[2]
	xIdx := binary.LittleEndian.Uint16(data[3:])
	yIdx := binary.LittleEndian.Uint16(data[5:])
	// flags := data[7]
	yLen := binary.LittleEndian.Uint16(data[8:])
	cbLen := binary.LittleEndian.Uint16(data[10:])
	crLen := binary.LittleEndian.Uint16(data[12:])
	// tailLen := binary.LittleEndian.Uint16(data[14:])

	off := 16
	yData := safeSlice(data, off, int(yLen))
	off += int(yLen)
	cbData := safeSlice(data, off, int(cbLen))
	off += int(cbLen)
	crData := safeSlice(data, off, int(crLen))

	qY := rfxGetQuant(quants, int(quantIdxY))
	qCb := rfxGetQuant(quants, int(quantIdxCb))
	qCr := rfxGetQuant(quants, int(quantIdxCr))

	yPixels := rfxDecodeComponent(yData, qY, 1)
	cbPixels := rfxDecodeComponent(cbData, qCb, 1)
	crPixels := rfxDecodeComponent(crData, qCr, 1)

	rfxPlaceTile(yPixels, cbPixels, crPixels, int(xIdx), int(yIdx), output, outW, outH)

	coeffPool.Put(yPixels)
	coeffPool.Put(cbPixels)
	coeffPool.Put(crPixels)
}

// decodeTileFirst handles PROGRESSIVE_WBT_TILE_FIRST (0xCCC6).
func (d *rfxProgressiveDecoder) decodeTileFirst(data []byte, quants []rfxQuant, output []byte, outW, outH int) {
	if len(data) < 17 {
		return
	}

	quantIdxY := data[0]
	quantIdxCb := data[1]
	quantIdxCr := data[2]
	xIdx := binary.LittleEndian.Uint16(data[3:])
	yIdx := binary.LittleEndian.Uint16(data[5:])
	// flags := data[7]
	// quality := data[8]
	yLen := binary.LittleEndian.Uint16(data[9:])
	cbLen := binary.LittleEndian.Uint16(data[11:])
	crLen := binary.LittleEndian.Uint16(data[13:])
	// tailLen := binary.LittleEndian.Uint16(data[15:])

	off := 17
	yData := safeSlice(data, off, int(yLen))
	off += int(yLen)
	cbData := safeSlice(data, off, int(cbLen))
	off += int(cbLen)
	crData := safeSlice(data, off, int(crLen))

	qY := rfxGetQuant(quants, int(quantIdxY))
	qCb := rfxGetQuant(quants, int(quantIdxCb))
	qCr := rfxGetQuant(quants, int(quantIdxCr))

	yPixels := rfxDecodeComponent(yData, qY, 1)
	cbPixels := rfxDecodeComponent(cbData, qCb, 1)
	crPixels := rfxDecodeComponent(crData, qCr, 1)

	rfxPlaceTile(yPixels, cbPixels, crPixels, int(xIdx), int(yIdx), output, outW, outH)

	coeffPool.Put(yPixels)
	coeffPool.Put(cbPixels)
	coeffPool.Put(crPixels)
}

func rfxGetQuant(quants []rfxQuant, idx int) rfxQuant {
	if idx < len(quants) {
		return quants[idx]
	}
	return rfxQuant{6, 6, 6, 6, 6, 6, 6, 6, 6, 6}
}

func safeSlice(data []byte, offset, length int) []byte {
	if length <= 0 || offset < 0 || offset+length > len(data) {
		return nil
	}
	return data[offset : offset+length]
}

// rfxDecodeComponent decodes one color component (Y, Cb, or Cr) for a 64×64 tile.
func rfxDecodeComponent(data []byte, quant rfxQuant, rlgrMode int) []int16 {
	const tilePixels = rfxTileSize * rfxTileSize // 4096

	// Reuse a pooled coefficient buffer to avoid per-tile allocation.
	coeffs := coeffPool.Get().([]int16)

	if data == nil {
		clear(coeffs)
		return coeffs
	}

	// 1. RLGR entropy decode → 4096 coefficients
	if rlgrMode == 3 {
		coeffs = rlgr3Decode(data, tilePixels, coeffs)
	} else {
		coeffs = rlgr1Decode(data, tilePixels, coeffs)
	}

	// 2. Differential decode LL3 (positions 4032..4095)
	for i := 4033; i < 4096; i++ {
		coeffs[i] += coeffs[i-1]
	}

	// 3. Dequantize (left-shift by quant-1 per subband)
	rfxDequantize(coeffs, quant)

	// 4. Inverse DWT (3 levels)
	rfxInverseDWT2D(coeffs)

	return coeffs
}

// rfxDequantize applies dequantization (left shift by factor-1) per subband.
func rfxDequantize(coeffs []int16, q rfxQuant) {
	rfxShiftSubband(coeffs[0:1024], q.HL1)    // HL1
	rfxShiftSubband(coeffs[1024:2048], q.LH1) // LH1
	rfxShiftSubband(coeffs[2048:3072], q.HH1) // HH1
	rfxShiftSubband(coeffs[3072:3328], q.HL2) // HL2
	rfxShiftSubband(coeffs[3328:3584], q.LH2) // LH2
	rfxShiftSubband(coeffs[3584:3840], q.HH2) // HH2
	rfxShiftSubband(coeffs[3840:3904], q.HL3) // HL3
	rfxShiftSubband(coeffs[3904:3968], q.LH3) // LH3
	rfxShiftSubband(coeffs[3968:4032], q.HH3) // HH3
	rfxShiftSubband(coeffs[4032:4096], q.LL3) // LL3
}

func rfxShiftSubband(data []int16, factor uint8) {
	if factor <= 1 {
		return
	}
	shift := factor - 1
	for i := range data {
		data[i] <<= shift
	}
}

// rfxInverseDWT2D performs 3-level inverse 2D discrete wavelet transform in-place.
// Buffer layout: [HL1(1024)|LH1(1024)|HH1(1024)|HL2(256)|LH2(256)|HH2(256)|HL3(64)|LH3(64)|HH3(64)|LL3(64)]
func rfxInverseDWT2D(coeffs []int16) {
	// Level 3: 8×8 subbands → 16×16 output
	rfxIDWT2DLevel(coeffs[3840:], 8)
	// Level 2: 16×16 subbands → 32×32 output
	rfxIDWT2DLevel(coeffs[3072:], 16)
	// Level 1: 32×32 subbands → 64×64 output
	rfxIDWT2DLevel(coeffs[0:], 32)
}

// rfxIDWT2DLevel performs one level of inverse 2D DWT.
// buf contains [HL(n²)|LH(n²)|HH(n²)|LL(n²)] and is replaced with the (2n)×(2n) result.
// Uses the MS-RDPRFX lifting scheme. Order: horizontal IDWT first, then vertical.
func rfxIDWT2DLevel(buf []int16, n int) {
	nn := n * n
	size := 2 * n

	bufs := idwtBufPool.Get().(*idwtBufs)
	hl := bufs.sub[0][:nn]
	lh := bufs.sub[1][:nn]
	hh := bufs.sub[2][:nn]
	ll := bufs.sub[3][:nn]
	copy(hl, buf[0:nn])
	copy(lh, buf[nn:2*nn])
	copy(hh, buf[2*nn:3*nn])
	copy(ll, buf[3*nn:4*nn])

	tmp := bufs.tmp[:size*size]

	// Step 1: Horizontal IDWT on each row
	for row := 0; row < n; row++ {
		rowOff := row * n
		lDstOff := row * size
		hDstOff := (row + n) * size

		tmp[lDstOff] = ll[rowOff] - int16((int32(hl[rowOff])+int32(hl[rowOff])+1)>>1)
		tmp[hDstOff] = lh[rowOff] - int16((int32(hh[rowOff])+int32(hh[rowOff])+1)>>1)

		for col := 1; col < n; col++ {
			x := col << 1
			tmp[lDstOff+x] = ll[rowOff+col] - int16((int32(hl[rowOff+col-1])+int32(hl[rowOff+col])+1)>>1)
			tmp[hDstOff+x] = lh[rowOff+col] - int16((int32(hh[rowOff+col-1])+int32(hh[rowOff+col])+1)>>1)
		}

		for col := 0; col < n-1; col++ {
			x := col << 1
			ld := (int32(hl[rowOff+col]) << 1) + ((int32(tmp[lDstOff+x]) + int32(tmp[lDstOff+x+2])) >> 1)
			hd := (int32(hh[rowOff+col]) << 1) + ((int32(tmp[hDstOff+x]) + int32(tmp[hDstOff+x+2])) >> 1)
			tmp[lDstOff+x+1] = int16(ld)
			tmp[hDstOff+x+1] = int16(hd)
		}
		x := (n - 1) << 1
		ld := (int32(hl[rowOff+n-1]) << 1) + int32(tmp[lDstOff+x])
		hd := (int32(hh[rowOff+n-1]) << 1) + int32(tmp[hDstOff+x])
		tmp[lDstOff+x+1] = int16(ld)
		tmp[hDstOff+x+1] = int16(hd)
	}

	// Step 2: Vertical IDWT on each column.
	// Process 4 columns at a time to improve cache utilisation (each row
	// access loads a cache line that covers 4+ consecutive int16 values).
	const blk = 4
	col := 0
	for ; col+blk <= size; col += blk {
		for b := 0; b < blk; b++ {
			c := col + b
			lVal := int32(tmp[c])
			hVal := int32(tmp[n*size+c])
			buf[c] = int16(lVal - ((hVal*2 + 1) >> 1))
		}
		for row := 1; row < n; row++ {
			for b := 0; b < blk; b++ {
				c := col + b
				lIdx := row*size + c
				hIdx := (row+n)*size + c
				hPrevIdx := (row-1+n)*size + c

				even := int32(tmp[lIdx]) - ((int32(tmp[hPrevIdx]) + int32(tmp[hIdx]) + 1) >> 1)
				buf[2*row*size+c] = int16(even)

				prevEven := int32(buf[(2*row-2)*size+c])
				odd := (int32(tmp[hPrevIdx]) << 1) + ((prevEven + even) >> 1)
				buf[(2*row-1)*size+c] = int16(odd)
			}
		}
		for b := 0; b < blk; b++ {
			c := col + b
			lastEven := int32(buf[(2*n-2)*size+c])
			lastH := int32(tmp[(2*n-1)*size+c])
			buf[(2*n-1)*size+c] = int16((lastH << 1) + lastEven)
		}
	}
	for ; col < size; col++ {
		lVal := int32(tmp[col])
		hVal := int32(tmp[n*size+col])
		buf[col] = int16(lVal - ((hVal*2 + 1) >> 1))

		for row := 1; row < n; row++ {
			lIdx := row*size + col
			hIdx := (row+n)*size + col
			hPrevIdx := (row-1+n)*size + col

			even := int32(tmp[lIdx]) - ((int32(tmp[hPrevIdx]) + int32(tmp[hIdx]) + 1) >> 1)
			buf[2*row*size+col] = int16(even)

			prevEven := int32(buf[(2*row-2)*size+col])
			odd := (int32(tmp[hPrevIdx]) << 1) + ((prevEven + even) >> 1)
			buf[(2*row-1)*size+col] = int16(odd)
		}

		lastEven := int32(buf[(2*n-2)*size+col])
		lastH := int32(tmp[(2*n-1)*size+col])
		buf[(2*n-1)*size+col] = int16((lastH << 1) + lastEven)
	}
	idwtBufPool.Put(bufs)
}

// rfxPlaceTile converts YCbCr tile to BGRA using tile-grid indices (xIdx, yIdx).
func rfxPlaceTile(yCoeffs, cbCoeffs, crCoeffs []int16, xIdx, yIdx int, output []byte, outW, outH int) {
	rfxPlaceTileAbs(yCoeffs, cbCoeffs, crCoeffs, xIdx*rfxTileSize, yIdx*rfxTileSize, output, outW, outH)
}

// ictToBGRA converts n pixels from YCbCr (ICT) to BGRA and writes them into
// dst (which must hold ≥ 4*n bytes). Processing n pixels in [8]int32 fixed-size
// arrays lets the compiler emit ARM64 NEON / x86 AVX2 vectorised instructions.
func ictToBGRA(yRow, cbRow, crRow []int16, dst []byte, n int) {
	const batch = 8
	full := (n / batch) * batch
	for base := 0; base < full; base += batch {
		var yv, cb, cr [batch]int32
		for k := 0; k < batch; k++ {
			yv[k] = int32(yRow[base+k])
			cb[k] = int32(cbRow[base+k])
			cr[k] = int32(crRow[base+k])
		}
		for k := 0; k < batch; k++ {
			ys := (yv[k] + 4096) << 16
			i := (base + k) * 4
			dst[i] = byte(max(0, min((cb[k]*115992+ys)>>21, 255)))
			dst[i+1] = byte(max(0, min((ys-cb[k]*22527-cr[k]*46819)>>21, 255)))
			dst[i+2] = byte(max(0, min((cr[k]*91916+ys)>>21, 255)))
			dst[i+3] = 0xFF
		}
	}
	// Scalar tail for pixels that don't fill a full batch.
	for col := full; col < n; col++ {
		yv := int32(yRow[col])
		cb := int32(cbRow[col])
		cr := int32(crRow[col])
		ys := (yv + 4096) << 16
		i := col * 4
		dst[i] = byte(max(0, min((cb*115992+ys)>>21, 255)))
		dst[i+1] = byte(max(0, min((ys-cb*22527-cr*46819)>>21, 255)))
		dst[i+2] = byte(max(0, min((cr*91916+ys)>>21, 255)))
		dst[i+3] = 0xFF
	}
}

// rfxPlaceTileAbs converts YCbCr tile to BGRA and writes into the output buffer
// at absolute pixel coordinates (tileX, tileY).
// Uses ICT (Irreversible Color Transform) from MS-RDPRFX.
func rfxPlaceTileAbs(yCoeffs, cbCoeffs, crCoeffs []int16, tileX, tileY int, output []byte, outW, outH int) {
	tileW := rfxTileSize
	tileH := rfxTileSize
	if tileX+tileW > outW {
		tileW = outW - tileX
	}
	if tileY+tileH > outH {
		tileH = outH - tileY
	}
	if tileW <= 0 || tileH <= 0 {
		return
	}

	for row := 0; row < tileH; row++ {
		dstStart := ((tileY+row)*outW + tileX) * 4
		dstEnd := dstStart + tileW*4
		if dstStart < 0 || dstEnd > len(output) {
			continue
		}
		dstRow := output[dstStart:dstEnd:dstEnd]
		srcOff := row * rfxTileSize
		ictToBGRA(
			yCoeffs[srcOff:srcOff+tileW:srcOff+tileW],
			cbCoeffs[srcOff:srcOff+tileW:srcOff+tileW],
			crCoeffs[srcOff:srcOff+tileW:srcOff+tileW],
			dstRow, tileW,
		)
	}
}
