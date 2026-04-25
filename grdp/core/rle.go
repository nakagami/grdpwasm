package core

import (
	"fmt"
	"sync"
	"unsafe"
)

func CVAL(p *[]uint8) int {
	a := int((*p)[0])
	*p = (*p)[1:]
	return a
}

func CVAL2(p *[]uint8, v *uint16) {
	*v = *((*uint16)(unsafe.Pointer(&(*p)[0])))
	*p = (*p)[2:]
}

func CVAL3(p *[]uint8, v *[3]uint8) {
	(*v)[0] = (*p)[0]
	(*v)[1] = (*p)[1]
	(*v)[2] = (*p)[2]
	*p = (*p)[3:]
}

func REPEAT(f func(), count *int, x *int, width int) {
	for *count > 0 && *x < width {
		f()
		*count--
		*x++
	}
}

/* 1 byte bitmap decompress */
func decompress1(output *[]uint8, width, height int, input []uint8, size int) bool {
	var (
		prevline, line, count            int
		offset, code                     int
		x                                int = width
		opcode                           int
		lastopcode                       int8 = -1
		insertmix, bicolour, isfillormix bool
		mixmask, mask                    uint8
		colour1, colour2                 uint8
		mix                              uint8 = 0xff
		fom_mask                         uint8
	)
	out := *output
	for len(input) != 0 {
		fom_mask = 0
		code = CVAL(&input)
		opcode = code >> 4
		/* Handle different opcode forms */
		switch opcode {
		case 0xc, 0xd, 0xe:
			opcode -= 6
			count = int(code & 0xf)
			offset = 16
			break
		case 0xf:
			opcode = code & 0xf
			if opcode < 9 {
				count = int(CVAL(&input))
				count |= int(CVAL(&input) << 8)
			} else {
				count = 1
				if opcode < 0xb {
					count = 8
				}
			}
			offset = 0
			break
		default:
			opcode >>= 1
			count = int(code & 0x1f)
			offset = 32
			break
		}
		/* Handle strange cases for counts */
		if offset != 0 {
			isfillormix = ((opcode == 2) || (opcode == 7))
			if count == 0 {
				if isfillormix {
					count = int(CVAL(&input)) + 1
				} else {
					count = int(CVAL(&input) + offset)
				}
			} else if isfillormix {
				count <<= 3
			}
		}
		/* Read preliminary data */
		switch opcode {
		case 0: /* Fill */
			if (lastopcode == int8(opcode)) && !((x == width) && (prevline == 0)) {
				insertmix = true
			}
			break
		case 8: /* Bicolour */
			colour1 = uint8(CVAL(&input))
			colour2 = uint8(CVAL(&input))
			break
		case 3: /* Colour */
			colour2 = uint8(CVAL(&input))
			break
		case 6: /* SetMix/Mix */
			fallthrough
		case 7: /* SetMix/FillOrMix */
			mix = uint8(CVAL(&input))
			opcode -= 5
			break
		case 9: /* FillOrMix_1 */
			mask = 0x03
			opcode = 0x02
			fom_mask = 3
			break
		case 0x0a: /* FillOrMix_2 */
			mask = 0x05
			opcode = 0x02
			fom_mask = 5
			break
		}
		lastopcode = int8(opcode)
		mixmask = 0
		/* Output body */
		for count > 0 {
			if x >= width {
				if height <= 0 {
					return false
				}

				x = 0
				height--
				prevline = line
				line = height * width
			}
			switch opcode {
			case 0: /* Fill */
				if insertmix {
					if prevline == 0 {
						out[x+line] = mix
					} else {
						out[x+line] = out[prevline+x] ^ mix
					}
					insertmix = false
					count--
					x++
				}
				n := count
				if n > width-x {
					n = width - x
				}
				if prevline == 0 {
					clear(out[x+line : x+line+n])
				} else {
					copy(out[x+line:x+line+n], out[prevline+x:prevline+x+n])
				}
				count -= n
				x += n
				break
			case 1: /* Mix */
				n := count
				if n > width-x {
					n = width - x
				}
				if prevline == 0 {
					seg := out[x+line : x+line+n]
					for i := range seg {
						seg[i] = mix
					}
				} else {
					src := out[prevline+x : prevline+x+n]
					dst := out[x+line : x+line+n]
					for i := range dst {
						dst[i] = src[i] ^ mix
					}
				}
				count -= n
				x += n
				break
			case 2: /* Fill or Mix */
				if prevline == 0 {
					for count > 0 && x < width {
						mixmask <<= 1
						if mixmask == 0 {
							mask = fom_mask
							if fom_mask == 0 {
								mask = uint8(CVAL(&input))
								mixmask = 1
							}
						}
						if mask&mixmask != 0 {
							out[x+line] = mix
						} else {
							out[x+line] = 0
						}
					
						count--
						x++
					}
				} else {
					for count > 0 && x < width {
						mixmask = mixmask << 1
						if mixmask == 0 {
							mask = fom_mask
							if fom_mask == 0 {
								mask = uint8(CVAL(&input))
								mixmask = 1
							}
						}
						if mask&mixmask != 0 {
							out[x+line] = out[prevline+x] ^ mix
						} else {
							out[x+line] = out[prevline+x]
						}
					
						count--
						x++
					}
				}
				break
			case 3: /* Colour */
				n := count
				if n > width-x {
					n = width - x
				}
				seg := out[x+line : x+line+n]
				for i := range seg {
					seg[i] = colour2
				}
				count -= n
				x += n
				break
			case 4: /* Copy */
				for count > 0 && x < width {
					if len(input) == 0 {
						return false
					}
					out[x+line] = uint8(CVAL(&input))
				
					count--
					x++
				}
				break
			case 8: /* Bicolour */
				for count > 0 && x < width {
					if bicolour {
						out[x+line] = colour2
						bicolour = false
					} else {
						out[x+line] = colour1
						bicolour = true
						count++
					}
				
					count--
					x++
				}

				break

			case 0xd: /* White */
				n := count
				if n > width-x {
					n = width - x
				}
				seg := out[x+line : x+line+n]
				for i := range seg {
					seg[i] = 0xff
				}
				count -= n
				x += n
				break
			case 0xe: /* Black */
				n := count
				if n > width-x {
					n = width - x
				}
				clear(out[x+line : x+line+n])
				count -= n
				x += n
				break
			default:
				fmt.Printf("bitmap opcode 0x%x\n", opcode)
				return false
			}
		}
	}
	return true
}

// decompress2Pool reuses the intermediate []uint16 buffer across calls to
// decompress2, avoiding a large per-frame allocation.
var decompress2Pool sync.Pool

/* 2 byte bitmap decompress */
func decompress2(output *[]uint8, width, height int, input []uint8, size int) bool {
	needed := width * height

	var out []uint16
	if v := decompress2Pool.Get(); v != nil {
		out = v.([]uint16)
		if cap(out) < needed {
			out = make([]uint16, needed)
		} else {
			out = out[:needed]
		}
	} else {
		out = make([]uint16, needed)
	}
	defer func() { decompress2Pool.Put(out[:cap(out)]) }()

	var (
		prevline, line, count            int
		offset, code                     int
		x                                int = width
		opcode                           int
		lastopcode                       int = -1
		insertmix, bicolour, isfillormix bool
		mixmask, mask                    uint8
		colour1, colour2                 uint16
		mix                              uint16 = 0xffff
		fom_mask                         uint8
	)

	for len(input) != 0 {
		fom_mask = 0
		code = CVAL(&input)
		opcode = code >> 4
		/* Handle different opcode forms */
		switch opcode {
		case 0xc, 0xd, 0xe:
			opcode -= 6
			count = code & 0xf
			offset = 16
			break
		case 0xf:
			opcode = code & 0xf
			if opcode < 9 {
				count = CVAL(&input)
				count |= CVAL(&input) << 8
			} else {
				count = 1
				if opcode < 0xb {
					count = 8
				}
			}
			offset = 0
			break
		default:
			opcode >>= 1
			count = code & 0x1f
			offset = 32
			break
		}

		/* Handle strange cases for counts */
		if offset != 0 {
			isfillormix = ((opcode == 2) || (opcode == 7))
			if count == 0 {
				if isfillormix {
					count = CVAL(&input) + 1
				} else {
					count = CVAL(&input) + offset
				}
			} else if isfillormix {
				count <<= 3
			}
		}
		/* Read preliminary data */
		switch opcode {
		case 0: /* Fill */
			if (lastopcode == opcode) && !((x == width) && (prevline == 0)) {
				insertmix = true
			}
			break
		case 8: /* Bicolour */
			CVAL2(&input, &colour1)
			CVAL2(&input, &colour2)
			break
		case 3: /* Colour */
			CVAL2(&input, &colour2)
			break
		case 6: /* SetMix/Mix */
			fallthrough
		case 7: /* SetMix/FillOrMix */
			CVAL2(&input, &mix)
			opcode -= 5
			break
		case 9: /* FillOrMix_1 */
			mask = 0x03
			opcode = 0x02
			fom_mask = 3
			break
		case 0x0a: /* FillOrMix_2 */
			mask = 0x05
			opcode = 0x02
			fom_mask = 5
			break
		}
		lastopcode = opcode
		mixmask = 0
		/* Output body */
		for count > 0 {
			if x >= width {
				if height <= 0 {
					return false
				}

				x = 0
				height--
				prevline = line
				line = height * width
			}
			switch opcode {
			case 0: /* Fill */
				if insertmix {
					if prevline == 0 {
						out[x+line] = mix
					} else {
						out[x+line] = out[prevline+x] ^ mix
					}
					insertmix = false
					count--
					x++
				}
				n := count
				if n > width-x {
					n = width - x
				}
				if prevline == 0 {
					clear(out[x+line : x+line+n])
				} else {
					copy(out[x+line:x+line+n], out[prevline+x:prevline+x+n])
				}
				count -= n
				x += n
				break
			case 1: /* Mix */
				n := count
				if n > width-x {
					n = width - x
				}
				if prevline == 0 {
					seg := out[x+line : x+line+n]
					for i := range seg {
						seg[i] = mix
					}
				} else {
					src := out[prevline+x : prevline+x+n]
					dst := out[x+line : x+line+n]
					for i := range dst {
						dst[i] = src[i] ^ mix
					}
				}
				count -= n
				x += n
				break
			case 2: /* Fill or Mix */
				if prevline == 0 {
					for count > 0 && x < width {
						mixmask <<= 1
						if mixmask == 0 {
							mask = fom_mask
							if fom_mask == 0 {
								mask = uint8(CVAL(&input))
								mixmask = 1
							}
						}
						if mask&mixmask != 0 {
							out[x+line] = mix
						} else {
							out[x+line] = 0
						}
					
						count--
						x++
					}
				} else {
					for count > 0 && x < width {
						mixmask = mixmask << 1
						if mixmask == 0 {
							mask = fom_mask
							if fom_mask == 0 {
								mask = uint8(CVAL(&input))
								mixmask = 1
							}
						}
						if mask&mixmask != 0 {
							out[x+line] = out[prevline+x] ^ mix
						} else {
							out[x+line] = out[prevline+x]
						}
					
						count--
						x++
					}
				}
				break
			case 3: /* Colour */
				n := count
				if n > width-x {
					n = width - x
				}
				seg := out[x+line : x+line+n]
				for i := range seg {
					seg[i] = colour2
				}
				count -= n
				x += n
				break
			case 4: /* Copy */
				for count > 0 && x < width {
					var a uint16
					CVAL2(&input, &a)
					out[x+line] = a
				
					count--
					x++
				}

				break
			case 8: /* Bicolour */
				for count > 0 && x < width {
					if bicolour {
						out[x+line] = colour2
						bicolour = false
					} else {
						out[x+line] = colour1
						bicolour = true
						count++
					}
				
					count--
					x++
				}

				break
			case 0xd: /* White */
				n2 := count
				if n2 > width-x {
					n2 = width - x
				}
				seg2 := out[x+line : x+line+n2]
				for i := range seg2 {
					seg2[i] = 0xffff
				}
				count -= n2
				x += n2
				break
			case 0xe: /* Black */
				n3 := count
				if n3 > width-x {
					n3 = width - x
				}
				clear(out[x+line : x+line+n3])
				count -= n3
				x += n3
				break
			default:
				fmt.Printf("bitmap opcode 0x%x\n", opcode)
				return false
			}
		}
	}
	j := 0
	for _, v := range out {
		(*output)[j], (*output)[j+1] = PutUint16BE(v)
		j += 2
	}
	return true
}

// /* 3 byte bitmap decompress */
func decompress3(output *[]uint8, width, height int, input []uint8, size int) bool {
	var (
		prevline, line, count            int
		opcode, offset, code             int
		x                                int = width
		lastopcode                       int = -1
		insertmix, bicolour, isfillormix bool
		mixmask, mask                    uint8
		colour1                          = [3]uint8{0, 0, 0}
		colour2                          = [3]uint8{0, 0, 0}
		mix                              = [3]uint8{0xff, 0xff, 0xff}
		fom_mask                         uint8
	)
	out := *output
	for len(input) != 0 {
		fom_mask = 0
		code = CVAL(&input)
		opcode = code >> 4
		/* Handle different opcode forms */
		switch opcode {
		case 0xc, 0xd, 0xe:
			opcode -= 6
			count = code & 0xf
			offset = 16
			break
		case 0xf:
			opcode = code & 0xf
			if opcode < 9 {
				count = CVAL(&input)
				count |= CVAL(&input) << 8
			} else {
				count = 1
				if opcode < 0xb {
					count = 8
				}
			}
			offset = 0
			break
		default:
			opcode >>= 1
			count = code & 0x1f
			offset = 32
			break
		}

		/* Handle strange cases for counts */
		if offset != 0 {
			isfillormix = ((opcode == 2) || (opcode == 7))
			if count == 0 {
				if isfillormix {
					count = CVAL(&input) + 1
				} else {
					count = CVAL(&input) + offset
				}
			} else if isfillormix {
				count <<= 3
			}
		}
		/* Read preliminary data */
		switch opcode {
		case 0: /* Fill */
			if (lastopcode == opcode) && !((x == width) && (prevline == 0)) {
				insertmix = true
			}
			break
		case 8: /* Bicolour */
			CVAL3(&input, &colour1)
			CVAL3(&input, &colour2)
			break
		case 3: /* Colour */
			CVAL3(&input, &colour2)
			break
		case 6: /* SetMix/Mix */
			fallthrough
		case 7: /* SetMix/FillOrMix */
			CVAL3(&input, &mix)
			opcode -= 5
			break
		case 9: /* FillOrMix_1 */
			mask = 0x03
			opcode = 0x02
			fom_mask = 3
			break
		case 0x0a: /* FillOrMix_2 */
			mask = 0x05
			opcode = 0x02
			fom_mask = 5
			break
		}

		lastopcode = opcode
		mixmask = 0
		/* Output body */
		for count > 0 {
			if x >= width {
				if height <= 0 {
					return false
				}

				x = 0
				height--
				prevline = line
				line = height * width * 3
			}
			switch opcode {
			case 0: /* Fill */
				if insertmix {
					if prevline == 0 {
						out[3*x+line] = mix[0]
						out[3*x+line+1] = mix[1]
						out[3*x+line+2] = mix[2]
					} else {
						out[3*x+line] = out[prevline+3*x] ^ mix[0]
						out[3*x+line+1] = out[prevline+3*x+1] ^ mix[1]
						out[3*x+line+2] = out[prevline+3*x+2] ^ mix[2]
					}
					insertmix = false
					count--
					x++
				}
				n := count
				if n > width-x {
					n = width - x
				}
				if prevline == 0 {
					clear(out[3*x+line : 3*x+line+3*n])
				} else {
					dstBase := 3*x + line
					srcBase := prevline + 3*x
					copy(out[dstBase:dstBase+3*n], out[srcBase:srcBase+3*n])
				}
				count -= n
				x += n
				break
			case 1: /* Mix */
				n := count
				if n > width-x {
					n = width - x
				}
				dst1 := out[3*x+line : 3*x+line+3*n]
				if prevline == 0 {
					// Exponential-doubling copy: O(log n) memcpy calls
					dst1[0], dst1[1], dst1[2] = mix[0], mix[1], mix[2]
					for wrote := 3; wrote < len(dst1); {
						wrote += copy(dst1[wrote:], dst1[:wrote])
					}
				} else {
					src1 := out[prevline+3*x : prevline+3*x+3*n]
					for i := 0; i+2 < len(dst1); i += 3 {
						dst1[i] = src1[i] ^ mix[0]
						dst1[i+1] = src1[i+1] ^ mix[1]
						dst1[i+2] = src1[i+2] ^ mix[2]
					}
				}
				count -= n
				x += n
				break
			case 2: /* Fill or Mix */
				if prevline == 0 {
					base := 3*x + line
					for count > 0 && x < width {
						mixmask = mixmask << 1
						if mixmask == 0 {
							mask = fom_mask
							if fom_mask == 0 {
								mask = uint8(CVAL(&input))
								mixmask = 1
							}
						}
						if mask&mixmask != 0 {
							out[base] = mix[0]
							out[base+1] = mix[1]
							out[base+2] = mix[2]
						} else {
							out[base] = 0
							out[base+1] = 0
							out[base+2] = 0
						}
						base += 3
						count--
						x++
					}
				} else {
					base := 3*x + line
					prev := prevline + 3*x
					for count > 0 && x < width {
						mixmask = mixmask << 1
						if mixmask == 0 {
							mask = fom_mask
							if fom_mask == 0 {
								mask = uint8(CVAL(&input))
								mixmask = 1
							}
						}
						if mask&mixmask != 0 {
							out[base] = out[prev] ^ mix[0]
							out[base+1] = out[prev+1] ^ mix[1]
							out[base+2] = out[prev+2] ^ mix[2]
						} else {
							out[base] = out[prev]
							out[base+1] = out[prev+1]
							out[base+2] = out[prev+2]
						}
						base += 3
						prev += 3
						count--
						x++
					}
				}
				break
			case 3: /* Colour */
				n := count
				if n > width-x {
					n = width - x
				}
				seg3 := out[3*x+line : 3*x+line+3*n]
				seg3[0], seg3[1], seg3[2] = colour2[0], colour2[1], colour2[2]
				for wrote := 3; wrote < len(seg3); {
					wrote += copy(seg3[wrote:], seg3[:wrote])
				}
				count -= n
				x += n
				break
			case 4: /* Copy */
				base4 := 3*x + line
				for count > 0 && x < width {
					if len(input) < 3 {
						return false
					}
					out[base4] = uint8(CVAL(&input))
					out[base4+1] = uint8(CVAL(&input))
					out[base4+2] = uint8(CVAL(&input))
					base4 += 3
					count--
					x++
				}
				break
			case 8: /* Bicolour */
				base8 := 3*x + line
				for count > 0 && x < width {
					if bicolour {
						out[base8] = colour2[0]
						out[base8+1] = colour2[1]
						out[base8+2] = colour2[2]
						bicolour = false
					} else {
						out[base8] = colour1[0]
						out[base8+1] = colour1[1]
						out[base8+2] = colour1[2]
						bicolour = true
						count++
					}
					base8 += 3
					count--
					x++
				}
				break
			case 0xd: /* White */
				n3 := count
				if n3 > width-x {
					n3 = width - x
				}
				seg3 := out[3*x+line : 3*x+line+3*n3]
				for i := range seg3 {
					seg3[i] = 0xff
				}
				count -= n3
				x += n3
				break
			case 0xe: /* Black */
				n2 := count
				if n2 > width-x {
					n2 = width - x
				}
				clear(out[3*x+line : 3*x+line+3*n2])
				count -= n2
				x += n2
				break
			default:
				fmt.Printf("bitmap opcode 0x%x\n", opcode)
				return false
			}
		}
	}

	return true
}

/* decompress a colour plane */
func processPlane(in *[]uint8, width, height int, output *[]uint8, j int) int {
	var (
		indexw   int
		indexh   int
		code     int
		collen   int
		replen   int
		color    uint8
		x        uint8
		revcode  int
		lastline int
		thisline int
	)
	ln := len(*in)

	lastline = 0
	indexh = 0
	i := 0
	for indexh < height {
		thisline = j + (width * height * 4) - ((indexh + 1) * width * 4)
		color = 0
		indexw = 0
		i = thisline

		if lastline == 0 {
			for indexw < width {
				code = CVAL(in)
				replen = int(code & 0xf)
				collen = int((code >> 4) & 0xf)
				revcode = (replen << 4) | collen
				if (revcode <= 47) && (revcode >= 16) {
					replen = revcode
					collen = 0
				}
				for collen > 0 {
					color = uint8(CVAL(in))
					(*output)[i] = uint8(color)
					i += 4

					indexw++
					collen--
				}
				for replen > 0 {
					(*output)[i] = uint8(color)
					i += 4
					indexw++
					replen--
				}
			}
		} else {
			for indexw < width {
				code = CVAL(in)
				replen = int(code & 0xf)
				collen = int((code >> 4) & 0xf)
				revcode = (replen << 4) | collen
				if (revcode <= 47) && (revcode >= 16) {
					replen = revcode
					collen = 0
				}
				for collen > 0 {
					x = uint8(CVAL(in))
					if x&1 != 0 {
						x = x >> 1
						x = x + 1
						color = -x
					} else {
						x = x >> 1
						color = x
					}
					x = (*output)[indexw*4+lastline] + color
					(*output)[i] = uint8(x)
					i += 4
					indexw++
					collen--
				}
				for replen > 0 {
					x = (*output)[indexw*4+lastline] + color
					(*output)[i] = uint8(x)
					i += 4
					indexw++
					replen--
				}
			}
		}
		indexh++
		lastline = thisline
	}
	return ln - len(*in)
}

/* 4 byte bitmap decompress */
func decompress4(output *[]uint8, width, height int, input []uint8, size int) bool {
	var (
		code             int
		onceBytes, total int
	)

	code = CVAL(&input)
	rle := code&0x10 != 0
	noAlpha := code&0x20 != 0

	if !rle {
		return false
	}

	total = 1

	if noAlpha {
		// No alpha plane in the stream; fill alpha channel with 0xFF.
		for i := 3; i < len(*output); i += 4 {
			(*output)[i] = 0xFF
		}
	} else {
		onceBytes = processPlane(&input, width, height, output, 3)
		total += onceBytes
	}

	onceBytes = processPlane(&input, width, height, output, 2)
	total += onceBytes

	onceBytes = processPlane(&input, width, height, output, 1)
	total += onceBytes

	onceBytes = processPlane(&input, width, height, output, 0)
	total += onceBytes

	return true
}

// DecompressInto decompresses bitmap data into dst, reusing dst if it has
// sufficient capacity (size = width*height*bpp). If dst is nil or too small
// a new slice is allocated. Returns the (re)used output slice.
func DecompressInto(input []uint8, dst []uint8, width, height int, bpp int) []uint8 {
	size := width * height * bpp
	if cap(dst) >= size {
		dst = dst[:size]
	} else {
		dst = make([]uint8, size)
	}
	switch bpp {
	case 1:
		decompress1(&dst, width, height, input, size)
	case 2:
		decompress2(&dst, width, height, input, size)
	case 3:
		decompress3(&dst, width, height, input, size)
	case 4:
		decompress4(&dst, width, height, input, size)
	default:
		fmt.Printf("bpp %d\n", bpp)
	}
	return dst
}

/* main decompress function */
func Decompress(input []uint8, width, height int, bpp int) []uint8 {
	return DecompressInto(input, nil, width, height, bpp)
}
