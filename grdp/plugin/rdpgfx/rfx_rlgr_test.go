package rdpgfx

import (
	"testing"
)

// rlgr1Encode is a minimal RLGR1 encoder used only for round-trip tests.
// It implements the exact inverse of rlgr1Decode per MS-RDPRFX 3.1.8.1.7.3.
func rlgr1Encode(coeffs []int16) []byte {
	bw := &bitWriter{}
	k := uint32(1)
	kp := uint32(1 << rlgrLSGR)
	kr := uint32(1)
	krp := uint32(1 << rlgrLSGR)

	i := 0
	for i < len(coeffs) {
		if k > 0 {
			// RL mode: count zeros then encode non-zero value
			numZeros := uint32(0)
			for i+int(numZeros) < len(coeffs) && coeffs[i+int(numZeros)] == 0 {
				numZeros++
			}

			runMax := uint32(1) << k
			nGroups := uint32(0)
			run := numZeros
			for run >= runMax {
				bw.writeBit(0) // leading 0
				run -= runMax
				kp += rlgrUPGR
				if kp > rlgrKPMax {
					kp = rlgrKPMax
				}
				k = kp >> rlgrLSGR
				runMax = 1 << k
				nGroups++
			}
			bw.writeBit(1) // terminator
			if k > 0 {
				bw.writeBits(run, int(k))
			}
			i += int(numZeros)

			if i >= len(coeffs) {
				break
			}

			// Encode non-zero value
			val := coeffs[i]
			i++

			sign := uint32(0)
			mag := uint32(val)
			if val < 0 {
				sign = 1
				mag = uint32(-val)
			}
			bw.writeBit(sign) // sign bit

			code := mag - 1
			vk2 := code >> kr
			remainder := code & ((1 << kr) - 1)

			// leading 1-bits
			for j := uint32(0); j < vk2; j++ {
				bw.writeBit(1)
			}
			bw.writeBit(0) // terminator
			if kr > 0 {
				bw.writeBits(remainder, int(kr))
			}

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

			// Update k/kp
			if kp > rlgrDNGR {
				kp -= rlgrDNGR
			} else {
				kp = 0
			}
			k = kp >> rlgrLSGR

		} else {
			// GR mode: encode single value
			val := coeffs[i]
			i++

			var code uint32
			if val == 0 {
				code = 0
				kp += rlgrUQGR
				if kp > rlgrKPMax {
					kp = rlgrKPMax
				}
				k = kp >> rlgrLSGR
			} else {
				if val > 0 {
					code = uint32(val) * 2
				} else {
					code = uint32(-val)*2 - 1
				}
				if kp > rlgrDQGR {
					kp -= rlgrDQGR
				} else {
					kp = 0
				}
				k = kp >> rlgrLSGR
			}

			vk := code >> kr
			remainder := code & ((1 << kr) - 1)

			// leading 1-bits
			for j := uint32(0); j < vk; j++ {
				bw.writeBit(1)
			}
			bw.writeBit(0) // terminator
			if kr > 0 {
				bw.writeBits(remainder, int(kr))
			}

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
		}
	}

	return bw.bytes()
}

type bitWriter struct {
	data    []byte
	bitPos  int // 0..7, bits written in current byte
	current byte
}

func (bw *bitWriter) writeBit(b uint32) {
	bw.current = (bw.current << 1) | byte(b&1)
	bw.bitPos++
	if bw.bitPos == 8 {
		bw.data = append(bw.data, bw.current)
		bw.current = 0
		bw.bitPos = 0
	}
}

func (bw *bitWriter) writeBits(val uint32, n int) {
	for i := n - 1; i >= 0; i-- {
		bw.writeBit((val >> uint(i)) & 1)
	}
}

func (bw *bitWriter) bytes() []byte {
	if bw.bitPos > 0 {
		bw.data = append(bw.data, bw.current<<uint(8-bw.bitPos))
	}
	return bw.data
}

func TestRLGR1RoundTrip_AllZeros(t *testing.T) {
	// 4096 all-zero coefficients
	coeffs := make([]int16, 4096)
	encoded := rlgr1Encode(coeffs)
	t.Logf("All zeros: encoded to %d bytes", len(encoded))

	decoded := rlgr1Decode(encoded, 4096, nil)
	for i, v := range decoded {
		if v != 0 {
			t.Errorf("Position %d: expected 0, got %d", i, v)
		}
	}
}

func TestRLGR1RoundTrip_SingleDC(t *testing.T) {
	// 4032 zeros, then DC value at LL3[0], then 63 zeros
	testCases := []struct {
		name string
		dc   int16
	}{
		{"DC=+3 (white quant)", 3},
		{"DC=-4 (black quant)", -4},
		{"DC=+127", 127},
		{"DC=-128", -128},
		{"DC=+1", 1},
		{"DC=-1", -1},
		{"DC=+50", 50},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			coeffs := make([]int16, 4096)
			coeffs[4032] = tc.dc

			encoded := rlgr1Encode(coeffs)
			t.Logf("Encoded to %d bytes (hex: % x)", len(encoded), encoded)

			decoded := rlgr1Decode(encoded, 4096, nil)

			for i, v := range decoded {
				if v != coeffs[i] {
					t.Errorf("Position %d: expected %d, got %d", i, coeffs[i], v)
					if i > 10 {
						t.Fatalf("Too many errors, stopping")
					}
				}
			}
			t.Logf("LL3[0]=%d (expected %d)", decoded[4032], tc.dc)
		})
	}
}

func TestRLGR1RoundTrip_MultipleNonZero(t *testing.T) {
	// Coefficients with non-zero values in various subbands
	coeffs := make([]int16, 4096)
	coeffs[0] = 5    // HL1[0]
	coeffs[100] = -3  // HL1[100]
	coeffs[1024] = 7  // LH1[0]
	coeffs[4032] = 10 // LL3[0]
	coeffs[4033] = 2  // LL3[1]

	encoded := rlgr1Encode(coeffs)
	t.Logf("Multi non-zero: encoded to %d bytes", len(encoded))

	decoded := rlgr1Decode(encoded, 4096, nil)

	for i, v := range decoded {
		if v != coeffs[i] {
			t.Errorf("Position %d: expected %d, got %d", i, coeffs[i], v)
		}
	}
}

func TestRLGR1Decode_KnownBytes(t *testing.T) {
	// Test with the all-zero Cb/Cr data (5 bytes) that the server sends
	// This should decode to all zeros
	// We'll encode all zeros and verify the decoder handles it
	coeffs := make([]int16, 4096)
	encoded := rlgr1Encode(coeffs)

	decoded := rlgr1Decode(encoded, 4096, nil)
	for i, v := range decoded {
		if v != 0 {
			t.Errorf("Position %d: expected 0, got %d", i, v)
		}
	}
}

// rlgr3Encode is a minimal RLGR3 encoder used only for round-trip tests.
// RLGR3 differs from RLGR1 only in GR mode: it encodes TWO values per code.
// Reference: FreeRDP rfx_rlgr.c (rfx_rlgr3_encode).
func rlgr3Encode(coeffs []int16) []byte {
	bw := &bitWriter{}
	k := uint32(1)
	kp := uint32(1 << rlgrLSGR)
	kr := uint32(1)
	krp := uint32(1 << rlgrLSGR)

	i := 0
	for i < len(coeffs) {
		if k > 0 {
			// RL mode: identical to RLGR1
			numZeros := uint32(0)
			for i+int(numZeros) < len(coeffs) && coeffs[i+int(numZeros)] == 0 {
				numZeros++
			}

			runMax := uint32(1) << k
			nGroups := uint32(0)
			run := numZeros
			for run >= runMax {
				bw.writeBit(0)
				run -= runMax
				nGroups++
				kp += rlgrUPGR
				if kp > rlgrKPMax {
					kp = rlgrKPMax
				}
				k = kp >> rlgrLSGR
				runMax = uint32(1) << k
			}
			bw.writeBit(1) // terminator
			if k > 0 {
				bw.writeBits(run, int(k))
			}
			i += int(numZeros)
			_ = nGroups

			if i >= len(coeffs) {
				break
			}

			// Encode the non-zero value with sign
			val := coeffs[i]
			i++

			var code uint32
			sign := uint32(0)
			if val < 0 {
				code = uint32(-val) - 1
				sign = 1
			} else {
				code = uint32(val) - 1
			}
			bw.writeBit(sign)

			vk := code >> kr
			remainder := code & ((1 << kr) - 1)
			for j := uint32(0); j < vk; j++ {
				bw.writeBit(1)
			}
			bw.writeBit(0)
			if kr > 0 {
				bw.writeBits(remainder, int(kr))
			}

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

			kp -= rlgrDNGR
			if kp > rlgrKPMax { // underflow
				kp = 0
			}
			k = kp >> rlgrLSGR

		} else {
			// GR mode: RLGR3 encodes TWO values per code
			var val1, val2 int16
			val1 = coeffs[i]
			i++
			if i < len(coeffs) {
				val2 = coeffs[i]
				i++
			}

			// Convert to 2*mag-sign encoding
			var u1, u2 uint32
			if val1 < 0 {
				u1 = uint32(-val1)*2 - 1
			} else {
				u1 = uint32(val1) * 2
			}
			if val2 < 0 {
				u2 = uint32(-val2)*2 - 1
			} else {
				u2 = uint32(val2) * 2
			}

			code := u1 + u2

			// GR encode the sum
			vk := code >> kr
			remainder := code & ((1 << kr) - 1)
			for j := uint32(0); j < vk; j++ {
				bw.writeBit(1)
			}
			bw.writeBit(0)
			if kr > 0 {
				bw.writeBits(remainder, int(kr))
			}

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

			// Write nIdx bits for val1
			nIdx := uint32(0)
			if code != 0 {
				nIdx = uint32(bitLen(code))
			}
			if nIdx > 0 {
				bw.writeBits(u1, int(nIdx))
			}

			// Update k/kp
			if u1 != 0 && u2 != 0 {
				if kp > 2*rlgrDQGR {
					kp -= 2 * rlgrDQGR
				} else {
					kp = 0
				}
				k = kp >> rlgrLSGR
			} else if u1 == 0 && u2 == 0 {
				kp += 2 * rlgrUQGR
				if kp > rlgrKPMax {
					kp = rlgrKPMax
				}
				k = kp >> rlgrLSGR
			}
		}
	}

	return bw.bytes()
}

// bitLen returns the number of bits needed to represent val (same as bits.Len).
func bitLen(val uint32) int {
	n := 0
	for val > 0 {
		n++
		val >>= 1
	}
	return n
}

func TestRLGR3RoundTrip_AllZeros(t *testing.T) {
	coeffs := make([]int16, 4096)
	encoded := rlgr3Encode(coeffs)
	t.Logf("All zeros: encoded to %d bytes", len(encoded))

	decoded := rlgr3Decode(encoded, 4096, nil)
	for i, v := range decoded {
		if v != 0 {
			t.Errorf("Position %d: expected 0, got %d", i, v)
		}
	}
}

func TestRLGR3RoundTrip_SingleDC(t *testing.T) {
	testCases := []struct {
		name string
		dc   int16
	}{
		{"DC=+3", 3},
		{"DC=-4", -4},
		{"DC=+127", 127},
		{"DC=-128", -128},
		{"DC=+1", 1},
		{"DC=-1", -1},
		{"DC=+50", 50},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			coeffs := make([]int16, 4096)
			coeffs[4032] = tc.dc

			encoded := rlgr3Encode(coeffs)
			t.Logf("Encoded to %d bytes (hex: % x)", len(encoded), encoded)

			decoded := rlgr3Decode(encoded, 4096, nil)

			for i, v := range decoded {
				if v != coeffs[i] {
					t.Errorf("Position %d: expected %d, got %d", i, coeffs[i], v)
					if i > 10 {
						t.Fatalf("Too many errors, stopping")
					}
				}
			}
			t.Logf("LL3[0]=%d (expected %d)", decoded[4032], tc.dc)
		})
	}
}

func TestRLGR3RoundTrip_MultipleNonZero(t *testing.T) {
	coeffs := make([]int16, 4096)
	coeffs[0] = 5
	coeffs[1] = -3
	coeffs[100] = 7
	coeffs[101] = -2
	coeffs[1024] = 10
	coeffs[4032] = 20
	coeffs[4033] = -15

	encoded := rlgr3Encode(coeffs)
	t.Logf("Multi non-zero: encoded to %d bytes", len(encoded))

	decoded := rlgr3Decode(encoded, 4096, nil)

	for i, v := range decoded {
		if v != coeffs[i] {
			t.Errorf("Position %d: expected %d, got %d", i, coeffs[i], v)
		}
	}
}

func TestRLGR3RoundTrip_ConsecutiveNonZero(t *testing.T) {
	// Test GR mode with many consecutive non-zero pairs
	coeffs := make([]int16, 64)
	for i := 0; i < 64; i++ {
		coeffs[i] = int16(i%7 - 3) // values: -3,-2,-1,0,1,2,3
	}

	encoded := rlgr3Encode(coeffs)
	t.Logf("Consecutive non-zero: encoded to %d bytes", len(encoded))

	decoded := rlgr3Decode(encoded, 64, nil)

	for i, v := range decoded {
		if v != coeffs[i] {
			t.Errorf("Position %d: expected %d, got %d", i, coeffs[i], v)
		}
	}
}

// BenchmarkRLGR1Decode benchmarks RLGR1 decode on a 64x64 coefficient block.
func BenchmarkRLGR1Decode(b *testing.B) {
// Create coefficients representative of a natural image tile (mostly zeros, some non-zero)
coeffs := make([]int16, 4096)
for i := range coeffs {
if i%17 == 0 {
coeffs[i] = int16(i%256 - 128)
}
}
data := rlgr1Encode(coeffs)

b.ResetTimer()
var dst []int16
for b.Loop() {
dst = rlgr1Decode(data, len(coeffs), dst)
}
_ = dst
}

// BenchmarkRLGR3Decode benchmarks RLGR3 decode on a 64x64 coefficient block.
func BenchmarkRLGR3Decode(b *testing.B) {
coeffs := make([]int16, 4096)
for i := range coeffs {
if i%17 == 0 {
coeffs[i] = int16(i%256 - 128)
}
}
data := rlgr3Encode(coeffs)

b.ResetTimer()
var dst []int16
for b.Loop() {
dst = rlgr3Decode(data, len(coeffs), dst)
}
_ = dst
}
