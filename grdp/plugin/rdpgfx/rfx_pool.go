package rdpgfx

import "sync"

// Buffer pools for RFX tile decoding to minimize allocations in the hot path.
// Each 64×64 tile needs 4096 int16 coefficients per component (Y, Cb, Cr)
// and several temporary buffers for the IDWT.

var coeffPool = sync.Pool{
	New: func() interface{} { return make([]int16, 4096) },
}

// idwtBufs holds reusable scratch buffers for one rfxIDWT2DLevel call.
type idwtBufs struct {
	sub [4][]int16 // hl, lh, hh, ll (max 1024 each at level 1)
	tmp []int16    // max 64*64 = 4096
}

var idwtBufPool = sync.Pool{
	New: func() interface{} {
		b := &idwtBufs{}
		for i := range b.sub {
			b.sub[i] = make([]int16, 1024)
		}
		b.tmp = make([]int16, 4096)
		return b
	},
}
