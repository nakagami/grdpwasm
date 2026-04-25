package rdpgfx

// RLGR1/RLGR3 (Run-Length Golomb-Rice) decoder for RFX codec.
// Reference: MS-RDPRFX 3.1.8.1.7.3 RLGR1/RLGR3 Pseudocode
// Matches FreeRDP's rfx_rlgr.c implementation.

import "math/bits"

const (
	rlgrLSGR  = 3  // shift count to convert kp to k
	rlgrKPMax = 80 // max value for kp or krp
	rlgrUPGR  = 4  // increase in kp after a zero run in RL mode
	rlgrDNGR  = 6  // decrease in kp after a nonzero symbol in RL mode
	rlgrUQGR  = 3  // increase in kp after zero symbol in GR mode
	rlgrDQGR  = 3  // decrease in kp after nonzero symbol in GR mode
)

// rlgr1Decode decodes RLGR1-encoded data into signed 16-bit DWT coefficients.
// If dst is non-nil and has sufficient capacity, it is reused (zeroed first).
func rlgr1Decode(data []byte, outputSize int, dst []int16) []int16 {
	var output []int16
	if cap(dst) >= outputSize {
		output = dst[:outputSize]
		clear(output)
	} else {
		output = make([]int16, outputSize)
	}
	br := &rlgrBitReader{data: data, total: len(data) * 8}
	cnt := 0

	k := uint32(1)
	kp := uint32(1 << rlgrLSGR) // 8
	kr := uint32(1)
	krp := uint32(1 << rlgrLSGR) // 8

	for br.remaining() > 0 && cnt < outputSize {
		if k > 0 {
			// RL (Run-Length) Mode

			// Count leading 0-bits → number of full run groups
			vk := br.countLeadingBits(0)
			if br.remaining() < 0 {
				break
			}

			// Each leading 0 adds (1 << k) to run, with k adapting upward
			run := uint32(0)
			for i := uint32(0); i < vk; i++ {
				run += 1 << k
				kp += rlgrUPGR
				if kp > rlgrKPMax {
					kp = rlgrKPMax
				}
				k = kp >> rlgrLSGR
			}

			// Read k bits for run remainder
			if br.remaining() < int(k) {
				break
			}
			if k > 0 {
				run += br.readBits(int(k))
			}

			// Read sign bit for the non-zero value
			if br.remaining() < 1 {
				break
			}
			sign := br.readBits(1)

			// Decode non-zero magnitude using GR code with leading 1-bits
			vk2 := br.countLeadingBits(1)
			if br.remaining() < 0 {
				break
			}

			// Read kr bits for code remainder
			if br.remaining() < int(kr) {
				break
			}
			code := uint32(0)
			if kr > 0 {
				code = br.readBits(int(kr))
			}
			code |= vk2 << kr

			// Update kr/krp
			if vk2 == 0 {
				if krp > 2 {
					krp -= 2
				} else {
					krp = 0
				}
				kr = krp >> rlgrLSGR
			} else if vk2 != 1 {
				krp += vk2
				if krp > rlgrKPMax {
					krp = rlgrKPMax
				}
				kr = krp >> rlgrLSGR
			}

			// Update k/kp (decrease after non-zero)
			if kp > rlgrDNGR {
				kp -= rlgrDNGR
			} else {
				kp = 0
			}
			k = kp >> rlgrLSGR

			// Compute magnitude (code + 1, guaranteed non-zero)
			mag := int16(code + 1)
			if sign != 0 {
				mag = -mag
			}

			// Output: run zeros (already 0 from init), then the non-zero value
			runEnd := cnt + int(run)
			if runEnd > outputSize {
				runEnd = outputSize
			}
			cnt = runEnd
			if cnt < outputSize {
				output[cnt] = mag
				cnt++
			}

		} else {
			// GR (Golomb-Rice) Mode

			// Count leading 1-bits
			vk := br.countLeadingBits(1)
			if br.remaining() < 0 {
				break
			}

			// Read kr bits for code remainder
			if br.remaining() < int(kr) {
				break
			}
			code := uint32(0)
			if kr > 0 {
				code = br.readBits(int(kr))
			}
			code |= vk << kr

			// Update kr/krp
			if vk == 0 {
				if krp > 2 {
					krp -= 2
				} else {
					krp = 0
				}
				kr = krp >> rlgrLSGR
			} else if vk != 1 {
				krp += vk
				if krp > rlgrKPMax {
					krp = rlgrKPMax
				}
				kr = krp >> rlgrLSGR
			}

			// RLGR1: sign embedded in code as code = 2*magnitude - sign
			if code == 0 {
				kp += rlgrUQGR
				if kp > rlgrKPMax {
					kp = rlgrKPMax
				}
				k = kp >> rlgrLSGR

				if cnt < outputSize {
					cnt++ // zero already set from init
				}
			} else {
				if kp > rlgrDQGR {
					kp -= rlgrDQGR
				} else {
					kp = 0
				}
				k = kp >> rlgrLSGR

				var mag int16
				if code&1 != 0 {
					// odd code → negative
					mag = -int16((code + 1) >> 1)
				} else {
					// even code → positive
					mag = int16(code >> 1)
				}
				if cnt < outputSize {
					output[cnt] = mag
					cnt++
				}
			}
		}
	}

	return output
}

// rlgr3Decode decodes RLGR3-encoded data into signed 16-bit DWT coefficients.
// RLGR3 differs from RLGR1 only in GR mode: it encodes/decodes TWO values
// per GR code by encoding their sum then splitting.
// Reference: MS-RDPRFX 3.1.8.1.7.3, FreeRDP rfx_rlgr.c
func rlgr3Decode(data []byte, outputSize int, dst []int16) []int16 {
	var output []int16
	if cap(dst) >= outputSize {
		output = dst[:outputSize]
		clear(output)
	} else {
		output = make([]int16, outputSize)
	}
	br := &rlgrBitReader{data: data, total: len(data) * 8}
	cnt := 0

	k := uint32(1)
	kp := uint32(1 << rlgrLSGR)
	kr := uint32(1)
	krp := uint32(1 << rlgrLSGR)

	for br.remaining() > 0 && cnt < outputSize {
		if k > 0 {
			// RL Mode — identical to RLGR1
			vk := br.countLeadingBits(0)
			if br.remaining() < 0 {
				break
			}

			run := uint32(0)
			for i := uint32(0); i < vk; i++ {
				run += 1 << k
				kp += rlgrUPGR
				if kp > rlgrKPMax {
					kp = rlgrKPMax
				}
				k = kp >> rlgrLSGR
			}

			if br.remaining() < int(k) {
				break
			}
			if k > 0 {
				run += br.readBits(int(k))
			}

			if br.remaining() < 1 {
				break
			}
			sign := br.readBits(1)

			vk2 := br.countLeadingBits(1)
			if br.remaining() < 0 {
				break
			}

			if br.remaining() < int(kr) {
				break
			}
			code := uint32(0)
			if kr > 0 {
				code = br.readBits(int(kr))
			}
			code |= vk2 << kr

			if vk2 == 0 {
				if krp > 2 {
					krp -= 2
				} else {
					krp = 0
				}
				kr = krp >> rlgrLSGR
			} else if vk2 != 1 {
				krp += vk2
				if krp > rlgrKPMax {
					krp = rlgrKPMax
				}
				kr = krp >> rlgrLSGR
			}

			if kp > rlgrDNGR {
				kp -= rlgrDNGR
			} else {
				kp = 0
			}
			k = kp >> rlgrLSGR

			mag := int16(code + 1)
			if sign != 0 {
				mag = -mag
			}

			runEnd3 := cnt + int(run)
			if runEnd3 > outputSize {
				runEnd3 = outputSize
			}
			cnt = runEnd3
			if cnt < outputSize {
				output[cnt] = mag
				cnt++
			}

		} else {
			// GR Mode — RLGR3 variant: decode TWO values from one GR code
			vk := br.countLeadingBits(1)
			if br.remaining() < 0 {
				break
			}

			if br.remaining() < int(kr) {
				break
			}
			code := uint32(0)
			if kr > 0 {
				code = br.readBits(int(kr))
			}
			code |= vk << kr

			if vk == 0 {
				if krp > 2 {
					krp -= 2
				} else {
					krp = 0
				}
				kr = krp >> rlgrLSGR
			} else if vk != 1 {
				krp += vk
				if krp > rlgrKPMax {
					krp = rlgrKPMax
				}
				kr = krp >> rlgrLSGR
			}

			// RLGR3: code = val1 + val2 (sum of two 2*mag-sign encoded values)
			// Read nIdx bits to split: nIdx = bit-length of code
			nIdx := uint32(0)
			if code != 0 {
				nIdx = uint32(bits.Len(uint(code)))
			}

			if br.remaining() < int(nIdx) {
				break
			}
			val1 := uint32(0)
			if nIdx > 0 {
				val1 = br.readBits(int(nIdx))
			}
			val2 := code - val1

			// Update k/kp based on both values
			if val1 != 0 && val2 != 0 {
				if kp > 2*rlgrDQGR {
					kp -= 2 * rlgrDQGR
				} else {
					kp = 0
				}
				k = kp >> rlgrLSGR
			} else if val1 == 0 && val2 == 0 {
				kp += 2 * rlgrUQGR
				if kp > rlgrKPMax {
					kp = rlgrKPMax
				}
				k = kp >> rlgrLSGR
			}

			// Decode val1 as 2*mag-sign
			var mag1 int16
			if val1&1 != 0 {
				mag1 = -int16((val1 + 1) >> 1)
			} else {
				mag1 = int16(val1 >> 1)
			}
			if cnt < outputSize {
				output[cnt] = mag1
				cnt++
			}

			// Decode val2 as 2*mag-sign
			var mag2 int16
			if val2&1 != 0 {
				mag2 = -int16((val2 + 1) >> 1)
			} else {
				mag2 = int16(val2 >> 1)
			}
			if cnt < outputSize {
				output[cnt] = mag2
				cnt++
			}
		}
	}

	return output
}

// rlgrBitReader reads bits MSB-first from a byte slice.
//
// To minimise the per-bit cost on the RLGR hot path we keep a 64-bit
// shift-register (`acc`, MSB-aligned with `bitsInAcc` valid bits at the top)
// fed from `data[bytePos:]`. Reads up to 32 bits are a shift+mask, and runs
// of identical bits are extracted with a single `bits.LeadingZeros64`.
type rlgrBitReader struct {
	data      []byte
	bytePos   int    // next byte to load into acc
	acc       uint64 // bits aligned to MSB
	bitsInAcc int    // number of valid bits in acc (MSB-aligned)
	total     int    // total bits available (data*8)
	read      int    // bits consumed
}

func (br *rlgrBitReader) remaining() int {
	return br.total - br.read
}

// fill loads bytes into the high end of acc until at least `need` bits are
// buffered or the input is exhausted. need must be <= 56.
func (br *rlgrBitReader) fill(need int) {
	for br.bitsInAcc < need && br.bytePos < len(br.data) {
		br.acc |= uint64(br.data[br.bytePos]) << uint(56-br.bitsInAcc)
		br.bytePos++
		br.bitsInAcc += 8
	}
}

func (br *rlgrBitReader) readBits(n int) uint32 {
	if n <= 0 {
		return 0
	}
	if br.bitsInAcc < n {
		br.fill(n)
	}
	avail := br.bitsInAcc
	if avail > n {
		avail = n
	}
	var val uint32
	if avail > 0 {
		val = uint32(br.acc >> uint(64-avail))
		br.acc <<= uint(avail)
		br.bitsInAcc -= avail
		br.read += avail
	}
	if avail < n {
		// Past EOF: pad with zeros, but mark them as consumed so
		// subsequent remaining() accurately reports underflow.
		val <<= uint(n - avail)
		br.read += n - avail
	}
	return val
}

// countLeadingBits counts consecutive bits matching 'target' (0 or 1) and
// then consumes (skips) the terminator bit of the opposite value.
func (br *rlgrBitReader) countLeadingBits(target uint32) uint32 {
	count := uint32(0)
	for {
		if br.bitsInAcc < 56 && br.bytePos < len(br.data) {
			br.fill(56)
		}
		if br.bitsInAcc == 0 {
			return count
		}
		var run int
		if target == 0 {
			// Run of 0s ends at the first 1 bit.
			lz := bits.LeadingZeros64(br.acc)
			if lz >= br.bitsInAcc {
				// Entire buffered window is zeros — consume all and refill.
				count += uint32(br.bitsInAcc)
				br.read += br.bitsInAcc
				br.acc = 0
				br.bitsInAcc = 0
				continue
			}
			run = lz
		} else {
			// Run of 1s ends at the first 0 bit. Inverting maps it to LZ.
			lo := bits.LeadingZeros64(^br.acc)
			if lo >= br.bitsInAcc {
				count += uint32(br.bitsInAcc)
				br.read += br.bitsInAcc
				br.acc = 0
				br.bitsInAcc = 0
				continue
			}
			run = lo
		}
		// Consume `run` matching bits plus the single terminator bit.
		consume := run + 1
		count += uint32(run)
		br.acc <<= uint(consume)
		br.bitsInAcc -= consume
		br.read += consume
		return count
	}
}
