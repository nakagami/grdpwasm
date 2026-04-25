package rdpgfx

// ZGFX (RDP8 Bulk Compression) decompressor.
// Implements the decompression algorithm described in MS-RDPEGFX 2.2.4 / 3.3.8.
// Based on FreeRDP's reference implementation (libfreerdp/codec/zgfx.c).

const zgfxHistorySize = 2500000

type zgfxContext struct {
	history    []byte
	historyIdx int
}

func newZgfxContext() *zgfxContext {
	return &zgfxContext{
		history: make([]byte, zgfxHistorySize),
	}
}

// Huffman token types
const (
	tokenLiteral = 0
	tokenMatch   = 1
)

type zgfxToken struct {
	prefixLen  uint8
	prefixCode uint16
	valueBits  uint8
	tokenType  uint8
	valueBase  uint32
}

// Fixed Huffman table from FreeRDP zgfx.c (Apache-2.0 licensed).
var zgfxTokenTable = []zgfxToken{
	{1, 0, 8, tokenLiteral, 0},         // 0
	{5, 17, 5, tokenMatch, 0},          // 10001
	{5, 18, 7, tokenMatch, 32},         // 10010
	{5, 19, 9, tokenMatch, 160},        // 10011
	{5, 20, 10, tokenMatch, 672},       // 10100
	{5, 21, 12, tokenMatch, 1696},      // 10101
	{5, 24, 0, tokenLiteral, 0x00},     // 11000
	{5, 25, 0, tokenLiteral, 0x01},     // 11001
	{6, 44, 14, tokenMatch, 5792},      // 101100
	{6, 45, 15, tokenMatch, 22176},     // 101101
	{6, 52, 0, tokenLiteral, 0x02},     // 110100
	{6, 53, 0, tokenLiteral, 0x03},     // 110101
	{6, 54, 0, tokenLiteral, 0xFF},     // 110110
	{7, 92, 18, tokenMatch, 54944},     // 1011100
	{7, 93, 20, tokenMatch, 317088},    // 1011101
	{7, 110, 0, tokenLiteral, 0x04},    // 1101110
	{7, 111, 0, tokenLiteral, 0x05},    // 1101111
	{7, 112, 0, tokenLiteral, 0x06},    // 1110000
	{7, 113, 0, tokenLiteral, 0x07},    // 1110001
	{7, 114, 0, tokenLiteral, 0x08},    // 1110010
	{7, 115, 0, tokenLiteral, 0x09},    // 1110011
	{7, 116, 0, tokenLiteral, 0x0A},    // 1110100
	{7, 117, 0, tokenLiteral, 0x0B},    // 1110101
	{7, 118, 0, tokenLiteral, 0x3A},    // 1110110
	{7, 119, 0, tokenLiteral, 0x3B},    // 1110111
	{7, 120, 0, tokenLiteral, 0x3C},    // 1111000
	{7, 121, 0, tokenLiteral, 0x3D},    // 1111001
	{7, 122, 0, tokenLiteral, 0x3E},    // 1111010
	{7, 123, 0, tokenLiteral, 0x3F},    // 1111011
	{7, 124, 0, tokenLiteral, 0x40},    // 1111100
	{7, 125, 0, tokenLiteral, 0x80},    // 1111101
	{8, 188, 20, tokenMatch, 1365664},  // 10111100
	{8, 189, 21, tokenMatch, 2414240},  // 10111101
	{8, 252, 0, tokenLiteral, 0x0C},    // 11111100
	{8, 253, 0, tokenLiteral, 0x38},    // 11111101
	{8, 254, 0, tokenLiteral, 0x39},    // 11111110
	{8, 255, 0, tokenLiteral, 0x66},    // 11111111
	{9, 380, 22, tokenMatch, 4511392},  // 101111100
	{9, 381, 23, tokenMatch, 8705696},  // 101111101
	{9, 382, 24, tokenMatch, 17094304}, // 101111110
}

// zgfxTokenLut maps the next 9 bits (MSB-first) of the input stream to the
// matching Huffman token entry.  Built once at init().  Replaces the per-bit
// linear scan over zgfxTokenTable that used to dominate ZGFX hot path
// profiles — typical RDPGFX traffic decodes thousands of tokens per frame.
type tokenLutEntry struct {
	prefixLen uint8 // 0 means "no token (truncated input)"
	valueBits uint8
	tokenType uint8
	valueBase uint32
}

var zgfxTokenLut [512]tokenLutEntry

func init() {
	for _, t := range zgfxTokenTable {
		// All 9-bit windows whose top prefixLen bits == prefixCode map to t.
		shift := uint(9 - t.prefixLen)
		base := uint32(t.prefixCode) << shift
		span := uint32(1) << shift
		for j := uint32(0); j < span; j++ {
			idx := base | j
			if zgfxTokenLut[idx].prefixLen == 0 {
				zgfxTokenLut[idx] = tokenLutEntry{
					prefixLen: t.prefixLen,
					valueBits: t.valueBits,
					tokenType: t.tokenType,
					valueBase: t.valueBase,
				}
			}
		}
	}
}

// bitReader reads bits MSB-first from a byte slice.
type bitReader struct {
	data          []byte
	bytePos       int
	bitPos        uint8  // bits remaining in current byte (8..1)
	bitsRemaining uint32 // total decodable bits remaining
}

func newBitReader(data []byte) *bitReader {
	br := &bitReader{data: data}
	if len(data) > 0 {
		br.bitPos = 8
	}
	return br
}

// newBitReaderWithCount creates a reader that tracks total decodable bits.
// The last byte of RDP8 compressed data encodes the number of padding bits
// to subtract: bitsAvailable = (len-1)*8 - lastByte.
func newBitReaderWithCount(data []byte) *bitReader {
	br := &bitReader{data: data}
	if len(data) < 2 {
		return br
	}
	br.bitPos = 8
	paddingBits := uint32(data[len(data)-1])
	totalBits := uint32(len(data)-1) * 8
	if paddingBits > totalBits {
		br.bitsRemaining = 0
	} else {
		br.bitsRemaining = totalBits - paddingBits
	}
	// Exclude the last byte from readable data
	br.data = data[:len(data)-1]
	return br
}

func (br *bitReader) hasBitsRemaining() bool {
	return br.bitsRemaining > 0
}

// peek9 returns up to 9 bits left-aligned as the high bits of a 9-bit value
// (i.e. bit 8 = next bit out of the stream).  Does not advance the reader.
// avail is the number of valid bits returned; if fewer than 9 bits are
// available the returned word is zero-padded on the right (LSB).
func (br *bitReader) peek9() (val uint32, avail uint8) {
	if br.bytePos >= len(br.data) {
		return 0, 0
	}
	// First byte: only the low br.bitPos bits are still unread.
	mask := uint32(1)<<br.bitPos - 1
	bits := uint32(br.data[br.bytePos]) & mask
	avail = br.bitPos
	idx := br.bytePos + 1
	for avail < 9 && idx < len(br.data) {
		bits = (bits << 8) | uint32(br.data[idx])
		avail += 8
		idx++
	}
	// Compute val using the original avail, so the next stream bits land
	// in the high positions of the 9-bit result (zero-padded on the right
	// when fewer than 9 bits are available).
	if avail >= 9 {
		val = (bits >> (uint(avail) - 9)) & 0x1FF
		avail = 9
	} else if avail > 0 {
		val = bits << (9 - avail)
	}
	// Then clamp the reported avail to bitsRemaining.  val keeps its high
	// bits valid; lower bits become don't-care padding.  decodeToken only
	// accepts a LUT entry whose prefixLen <= the (clamped) avail, so any
	// bits beyond bitsRemaining cannot influence the decoded prefix.
	if uint32(avail) > br.bitsRemaining {
		avail = uint8(br.bitsRemaining)
	}
	return
}

// consumeBits advances the reader by n bits, updating bytePos/bitPos and
// bitsRemaining.  Caller must ensure n <= bitsRemaining (LUT entries
// already include this guarantee for valid streams).
func (br *bitReader) consumeBits(n uint8) {
	if uint32(n) > br.bitsRemaining {
		n = uint8(br.bitsRemaining)
	}
	br.bitsRemaining -= uint32(n)
	for n >= br.bitPos {
		n -= br.bitPos
		br.bytePos++
		br.bitPos = 8
	}
	br.bitPos -= n
	if br.bitPos == 0 {
		br.bytePos++
		br.bitPos = 8
	}
}

func (br *bitReader) getBit() uint32 {
	if br.bytePos >= len(br.data) {
		return 0
	}
	br.bitPos--
	bit := uint32((br.data[br.bytePos] >> br.bitPos) & 1)
	if br.bitPos == 0 {
		br.bytePos++
		br.bitPos = 8
	}
	return bit
}

func (br *bitReader) getBits(n uint8) uint32 {
	if n == 0 {
		return 0
	}
	// Fast path: enough bits remain in the current byte.
	if n <= br.bitPos && br.bytePos < len(br.data) {
		br.bitPos -= n
		v := (uint32(br.data[br.bytePos]) >> br.bitPos) & ((1 << n) - 1)
		if br.bitPos == 0 {
			br.bytePos++
			br.bitPos = 8
		}
		return v
	}
	// Slow path: span byte boundaries (n can be up to 24).
	var result uint32
	for i := uint8(0); i < n; i++ {
		result = (result << 1) | br.getBit()
	}
	return result
}

// readBytes reads count raw bytes (byte-aligned). Used for unencoded sections.
func (br *bitReader) readBytes(count int) []byte {
	// Discard any partial-byte bits to align
	if br.bitPos != 8 && br.bitPos != 0 {
		br.bytePos++
		br.bitPos = 8
	}
	if br.bytePos+count > len(br.data) {
		count = len(br.data) - br.bytePos
	}
	if count <= 0 {
		return nil
	}
	out := make([]byte, count)
	copy(out, br.data[br.bytePos:br.bytePos+count])
	br.bytePos += count
	return out
}

// historyWrite copies data into the ring history buffer.  Hot path — called
// for every literal, match expansion, and unencoded raw block.  Uses bulk
// copy() instead of byte-by-byte modulo arithmetic.
func (z *zgfxContext) historyWrite(data []byte) {
	n := len(data)
	if n == 0 {
		return
	}
	if n >= zgfxHistorySize {
		copy(z.history, data[n-zgfxHistorySize:])
		z.historyIdx = 0
		return
	}
	end := z.historyIdx + n
	if end <= zgfxHistorySize {
		copy(z.history[z.historyIdx:end], data)
		z.historyIdx = end
		if z.historyIdx == zgfxHistorySize {
			z.historyIdx = 0
		}
	} else {
		first := zgfxHistorySize - z.historyIdx
		copy(z.history[z.historyIdx:], data[:first])
		copy(z.history[:n-first], data[first:])
		z.historyIdx = n - first
	}
}

func (z *zgfxContext) outputLiteral(b byte, out *[]byte) {
	z.history[z.historyIdx] = b
	z.historyIdx++
	if z.historyIdx == zgfxHistorySize {
		z.historyIdx = 0
	}
	*out = append(*out, b)
}

// outputMatch copies `count` bytes starting `distance` bytes back in the
// history into both the output and the history ring.  Self-referential
// matches (count > distance) are handled via byte-wise copy after the first
// pass, since the pattern grows as it is written.
func (z *zgfxContext) outputMatch(distance, count int, out *[]byte) {
	if distance <= 0 || count <= 0 {
		return
	}
	o := *out
	base := len(o)
	// Grow output by `count` bytes in a single allocation step.
	if cap(o)-base < count {
		// Standard slice-growth: at least double cap or fit count, whichever larger.
		newCap := cap(o) * 2
		if newCap < base+count {
			newCap = base + count
		}
		grown := make([]byte, base+count, newCap)
		copy(grown, o)
		o = grown
	} else {
		o = o[:base+count]
	}

	srcIdx := z.historyIdx - distance
	if srcIdx < 0 {
		srcIdx += zgfxHistorySize
	}

	if count <= distance {
		// Non-overlapping pattern: 1-2 contiguous copies from history.
		end := srcIdx + count
		if end <= zgfxHistorySize {
			copy(o[base:base+count], z.history[srcIdx:end])
		} else {
			first := zgfxHistorySize - srcIdx
			copy(o[base:base+first], z.history[srcIdx:])
			copy(o[base+first:base+count], z.history[:count-first])
		}
	} else {
		// Self-overlapping: copy the initial `distance` bytes from history,
		// then expand the pattern in-place within the output buffer (the
		// classic LZ77 overlapping copy).
		end := srcIdx + distance
		if end <= zgfxHistorySize {
			copy(o[base:base+distance], z.history[srcIdx:end])
		} else {
			first := zgfxHistorySize - srcIdx
			copy(o[base:base+first], z.history[srcIdx:])
			copy(o[base+first:base+distance], z.history[:distance-first])
		}
		// Overlapping in-place expansion (must be byte-wise; copy() does
		// not guarantee overlapping semantics for src==dst+offset).
		for i := distance; i < count; i++ {
			o[base+i] = o[base+i-distance]
		}
	}

	*out = o
	z.historyWrite(o[base : base+count])
}

// Decompress decompresses a ZGFX compressed segment payload.
// The payload must NOT include the 1-byte segment header (flags byte).
// In RDP8 ZGFX, the last byte of the payload encodes the number of
// padding bits to subtract from the total bit count.
//
// buf is an optional caller-supplied backing buffer (e.g. from a sync.Pool).
// The returned slice may use buf's backing array or a new one if buf was too
// small; callers that pool the output must use the RETURNED slice, not buf.
func (z *zgfxContext) Decompress(data []byte, buf []byte) []byte {
	if len(data) < 2 {
		// Need at least 1 byte of compressed data + 1 byte of padding count
		return nil
	}

	br := newBitReaderWithCount(data)
	// Use the caller-supplied buffer if it has enough capacity; otherwise fall
	// back to a fresh allocation sized at 3× the compressed input.
	estSize := len(data) * 3
	var out []byte
	if cap(buf) >= estSize {
		out = buf[:0]
	} else {
		out = make([]byte, 0, estSize)
	}

	for br.hasBitsRemaining() {
		token, ok := z.decodeToken(br)
		if !ok {
			break
		}
		// decodeToken already consumed prefixLen bits.

		if token.tokenType == tokenLiteral {
			if br.bitsRemaining < uint32(token.valueBits) {
				break
			}
			value := token.valueBase + br.getBits(token.valueBits)
			br.bitsRemaining -= uint32(token.valueBits)
			z.outputLiteral(byte(value), &out)
		} else {
			// Match token
			if br.bitsRemaining < uint32(token.valueBits) {
				break
			}
			distance := int(token.valueBase + br.getBits(token.valueBits))
			br.bitsRemaining -= uint32(token.valueBits)

			if distance != 0 {
				// Match: copy from history
				count := z.decodeMatchCount(br)
				z.outputMatch(distance, count, &out)
			} else {
				// Unencoded: read raw bytes
				if br.bitsRemaining < 15 {
					break
				}
				rawCount := int(br.getBits(15))
				br.bitsRemaining -= 15
				// Discard remaining bits in current byte to align to byte boundary
				// (equivalent to FreeRDP's cBitsCurrent = 0; BitsCurrent = 0;)
				if br.bitPos < 8 {
					br.bitsRemaining -= uint32(br.bitPos)
					br.bytePos++
					br.bitPos = 8
				}
				if br.bytePos+rawCount > len(br.data) || uint32(rawCount)*8 > br.bitsRemaining {
					break
				}
				rawBytes := br.data[br.bytePos : br.bytePos+rawCount]
				br.bytePos += rawCount
				br.bitsRemaining -= uint32(rawCount) * 8
				z.historyWrite(rawBytes)
				out = append(out, rawBytes...)
			}
		}
	}

	return out
}

// decodeToken consumes the next Huffman prefix (1..9 bits) from the input
// stream using the precomputed zgfxTokenLut.  Returns the decoded token and
// true on success; false if the remaining input is too short or malformed.
//
// Unlike the old implementation, this version no longer uses a per-bit
// linear search through the 38-entry token table — the dominant ZGFX cost
// during real RDPGFX traffic.
func (z *zgfxContext) decodeToken(br *bitReader) (zgfxToken, bool) {
	val, avail := br.peek9()
	if avail == 0 {
		return zgfxToken{}, false
	}
	e := zgfxTokenLut[val]
	if e.prefixLen == 0 || uint8(avail) < e.prefixLen {
		return zgfxToken{}, false
	}
	br.consumeBits(e.prefixLen)
	return zgfxToken{
		prefixLen:  e.prefixLen,
		valueBits:  e.valueBits,
		tokenType:  e.tokenType,
		valueBase:  e.valueBase,
	}, true
}

// decodeMatchCount decodes the match length using FreeRDP's algorithm:
//
//	0           → 3
//	10 + 2 bits → 4 + value   (4..7)
//	110 + 3 bits → 8 + value  (8..15)
//	1110 + 4 bits → 16 + value (16..31)
//	... and so on (each additional leading 1 doubles the base and adds 1 extra bit)
func (z *zgfxContext) decodeMatchCount(br *bitReader) int {
	bit := br.getBit()
	br.bitsRemaining--
	if bit == 0 {
		return 3
	}

	count := 4
	extra := uint8(2)

	bit = br.getBit()
	br.bitsRemaining--
	for bit == 1 {
		count <<= 1
		extra++
		bit = br.getBit()
		br.bitsRemaining--
	}

	if br.bitsRemaining < uint32(extra) {
		return count
	}
	count += int(br.getBits(extra))
	br.bitsRemaining -= uint32(extra)
	return count
}
