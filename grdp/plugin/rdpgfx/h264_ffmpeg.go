//go:build h264

package rdpgfx

/*
#cgo pkg-config: libavcodec libavutil libswscale
#include <libavcodec/avcodec.h>
#include <libavutil/imgutils.h>
#include <libavutil/hwcontext.h>
#include <libavutil/log.h>
#include <libswscale/swscale.h>
#include <stdlib.h>
#include <stdint.h>
#ifdef __ARM_NEON__
#include <arm_neon.h>
#endif

// grdp_suppress_av_log sets FFmpeg's global log level to FATAL so that
// decoder-level error messages (e.g. "sps_id out of range", "no frame!")
// are not printed to stderr.  Those messages are expected and harmless
// during H.264 stream recovery; grdp emits its own slog warnings instead.
static void grdp_suppress_av_log(void) {
    av_log_set_level(AV_LOG_FATAL);
}

// get_format callback that prefers the hardware pixel format stored in opaque.
static enum AVPixelFormat grdp_get_hw_format(
    AVCodecContext *ctx, const enum AVPixelFormat *pix_fmts) {
    enum AVPixelFormat hw_fmt = (enum AVPixelFormat)(intptr_t)ctx->opaque;
    if (hw_fmt == AV_PIX_FMT_NONE) return pix_fmts[0];
    for (const enum AVPixelFormat *p = pix_fmts; *p != AV_PIX_FMT_NONE; p++) {
        if (*p == hw_fmt) return *p;
    }
    return pix_fmts[0];
}

static void grdp_set_get_format(AVCodecContext *ctx) {
    ctx->get_format = grdp_get_hw_format;
}

// grdp_set_low_delay enables AV_CODEC_FLAG_LOW_DELAY on the codec context
// so the decoder emits frames as soon as they are decoded, without waiting
// to reorder B-frames.  RDP H.264 streams transmit in display order and do
// not use B-frame reordering, so the default reorder buffer only adds
// apparent latency and (on VideoToolbox) makes legitimate frames look like
// "null frames" to our stall detector, triggering spurious hard resets.
static void grdp_set_low_delay(AVCodecContext *ctx) {
    ctx->flags |= AV_CODEC_FLAG_LOW_DELAY;
    ctx->flags2 |= AV_CODEC_FLAG2_FAST;
}

static void grdp_set_hw_pix_fmt(AVCodecContext *ctx, enum AVPixelFormat fmt) {
    ctx->opaque = (void*)(intptr_t)fmt;
}

// Helper: convert AVFrame to BGRA via swscale.
static int grdp_frame_to_bgra(struct SwsContext *sws,
    AVFrame *src, uint8_t *dst, int dst_stride) {
    uint8_t *dst_data[4] = {dst, NULL, NULL, NULL};
    int dst_linesize[4] = {dst_stride, 0, 0, 0};
    return sws_scale(sws,
        (const uint8_t *const *)src->data, src->linesize,
        0, src->height,
        dst_data, dst_linesize);
}

// Map deprecated YUVJ pixel formats to their non-J equivalents.
// YUVJ formats are full-range YUV; the modern way is to use the plain YUV
// format and communicate the range via sws_setColorspaceDetails.
static enum AVPixelFormat grdp_yuvj_to_yuv(enum AVPixelFormat fmt) {
    switch (fmt) {
    case AV_PIX_FMT_YUVJ420P: return AV_PIX_FMT_YUV420P;
    case AV_PIX_FMT_YUVJ422P: return AV_PIX_FMT_YUV422P;
    case AV_PIX_FMT_YUVJ444P: return AV_PIX_FMT_YUV444P;
    case AV_PIX_FMT_YUVJ440P: return AV_PIX_FMT_YUV440P;
    default: return fmt;
    }
}

// Return 1 if fmt is a full-range (YUVJ) format, 0 otherwise.
static int grdp_is_full_range_fmt(enum AVPixelFormat fmt) {
    return (fmt == AV_PIX_FMT_YUVJ420P ||
            fmt == AV_PIX_FMT_YUVJ422P ||
            fmt == AV_PIX_FMT_YUVJ444P ||
            fmt == AV_PIX_FMT_YUVJ440P) ? 1 : 0;
}

// grdp_bt601_pixel writes one BGRA pixel using BT.601 coefficients.
// u and v are pre-offset (i.e. raw_value - 128).
// full_range: 0 = limited (video) range [16-235 / 16-240],
//             1 = full range [0-255].
#define CLAMP8(x) ((x) < 0 ? 0 : (x) > 255 ? 255 : (uint8_t)(x))
static inline void grdp_bt601_pixel(
    int y_raw, int u, int v, int full_range, uint8_t *dst)
{
    int r, g, b;
    if (full_range) {
        int y = y_raw;
        r = (256*y + 359*v           + 128) >> 8;
        g = (256*y -  88*u - 183*v   + 128) >> 8;
        b = (256*y + 454*u           + 128) >> 8;
    } else {
        int c = y_raw - 16;
        r = (298*c + 409*v           + 128) >> 8;
        g = (298*c - 100*u - 208*v   + 128) >> 8;
        b = (298*c + 516*u           + 128) >> 8;
    }
    dst[0] = CLAMP8(b);
    dst[1] = CLAMP8(g);
    dst[2] = CLAMP8(r);
    dst[3] = 255;
}

// grdp_yuv420p_to_bgra converts a planar YUV420P/YUVJ420P frame to packed
// BGRA using BT.601 coefficients.  This bypasses swscale entirely so that
// the broken ARM64 colorspace-matrix fallback path is never taken.
static void grdp_yuv420p_to_bgra(
    const AVFrame *src, uint8_t *dst, int dst_stride, int full_range)
{
    int width  = src->width;
    int height = src->height;
    for (int row = 0; row < height; row++) {
        const uint8_t *yrow = src->data[0] + row        * src->linesize[0];
        const uint8_t *urow = src->data[1] + (row >> 1) * src->linesize[1];
        const uint8_t *vrow = src->data[2] + (row >> 1) * src->linesize[2];
        uint8_t *drow = dst + row * dst_stride;
        for (int col = 0; col < width; col++) {
            int u = (int)urow[col >> 1] - 128;
            int v = (int)vrow[col >> 1] - 128;
            grdp_bt601_pixel((int)yrow[col], u, v, full_range, drow + col*4);
        }
    }
}

// grdp_nv12_to_bgra converts a semi-planar NV12 frame (Y plane + interleaved
// UV plane) to packed BGRA using BT.601 coefficients.  This bypasses swscale
// for the same reason as grdp_yuv420p_to_bgra: on ARM64 swscale's
// non-accelerated NV12→BGRA fallback ignores sws_setColorspaceDetails.
// VideoToolbox (macOS HW decoder) always outputs NV12.
//
// On ARM64 the inner loop is NEON-accelerated (8 pixels per iteration) to
// reduce per-frame CPU cost and decode-loop jitter.
#ifdef __ARM_NEON__
// grdp_nv12_to_bgra_neon_8 processes 8 luma pixels (4 UV pairs) per call.
// All int32x4_t intermediates prevent overflow of e.g. 298*239 = 71 222.
static inline void grdp_nv12_to_bgra_neon_8(
    const uint8_t *yrow, const uint8_t *uvrow, uint8_t *drow,
    int col, int16_t ky, int16_t kr, int16_t kgu, int16_t kgv, int16_t kb,
    int16_t yoff)
{
    // Load 8 luma bytes, convert to int16, subtract Y offset (16 or 0).
    uint8x8_t y_u8 = vld1_u8(yrow + col);
    int16x8_t c16  = vsubq_s16(vreinterpretq_s16_u16(vmovl_u8(y_u8)),
                                vdupq_n_s16(yoff));

    // Load 8 UV bytes: [U0,V0,U1,V1,U2,V2,U3,V3].
    // vuzp deinterleaves into .val[0]=[U0,U1,U2,U3,…] .val[1]=[V0,V1,V2,V3,…].
    // vzip then duplicates each value for the two luma pixels it serves.
    uint8x8_t uv_u8    = vld1_u8(uvrow + col);
    uint8x8x2_t uv_sep = vuzp_u8(uv_u8, uv_u8);
    uint8x8_t u8       = vzip_u8(uv_sep.val[0], uv_sep.val[0]).val[0]; // [U0,U0,U1,U1,…]
    uint8x8_t v8       = vzip_u8(uv_sep.val[1], uv_sep.val[1]).val[0]; // [V0,V0,V1,V1,…]

    int16x8_t u16 = vsubq_s16(vreinterpretq_s16_u16(vmovl_u8(u8)), vdupq_n_s16(128));
    int16x8_t v16 = vsubq_s16(vreinterpretq_s16_u16(vmovl_u8(v8)), vdupq_n_s16(128));

    // Compute R/G/B with int32 to avoid overflow.  Process 4+4 pixels.
    int16x4_t c_lo = vget_low_s16(c16),  u_lo = vget_low_s16(u16),  v_lo = vget_low_s16(v16);
    int16x4_t c_hi = vget_high_s16(c16), u_hi = vget_high_s16(u16), v_hi = vget_high_s16(v16);

    int32x4_t ky_lo = vmull_n_s16(c_lo, ky), ky_hi = vmull_n_s16(c_hi, ky);

    int32x4_t r_lo = vaddq_s32(vaddq_s32(ky_lo, vmull_n_s16(v_lo, kr)), vdupq_n_s32(128));
    int32x4_t g_lo = vaddq_s32(vsubq_s32(vsubq_s32(ky_lo, vmull_n_s16(u_lo, kgu)), vmull_n_s16(v_lo, kgv)), vdupq_n_s32(128));
    int32x4_t b_lo = vaddq_s32(vaddq_s32(ky_lo, vmull_n_s16(u_lo, kb)),  vdupq_n_s32(128));

    int32x4_t r_hi = vaddq_s32(vaddq_s32(ky_hi, vmull_n_s16(v_hi, kr)), vdupq_n_s32(128));
    int32x4_t g_hi = vaddq_s32(vsubq_s32(vsubq_s32(ky_hi, vmull_n_s16(u_hi, kgu)), vmull_n_s16(v_hi, kgv)), vdupq_n_s32(128));
    int32x4_t b_hi = vaddq_s32(vaddq_s32(ky_hi, vmull_n_s16(u_hi, kb)),  vdupq_n_s32(128));

    // Shift >>8, saturate int32→int16→uint8, then store interleaved BGRA.
    uint8x8_t r = vqmovun_s16(vcombine_s16(vqmovn_s32(vshrq_n_s32(r_lo,8)), vqmovn_s32(vshrq_n_s32(r_hi,8))));
    uint8x8_t g = vqmovun_s16(vcombine_s16(vqmovn_s32(vshrq_n_s32(g_lo,8)), vqmovn_s32(vshrq_n_s32(g_hi,8))));
    uint8x8_t b = vqmovun_s16(vcombine_s16(vqmovn_s32(vshrq_n_s32(b_lo,8)), vqmovn_s32(vshrq_n_s32(b_hi,8))));
    uint8x8x4_t bgra;
    bgra.val[0] = b;
    bgra.val[1] = g;
    bgra.val[2] = r;
    bgra.val[3] = vdup_n_u8(255);
    vst4_u8(drow + col * 4, bgra);
}
#endif // __ARM_NEON__

static void grdp_nv12_to_bgra(
    const AVFrame *src, uint8_t *dst, int dst_stride, int full_range)
{
    int width  = src->width;
    int height = src->height;
#ifdef __ARM_NEON__
    // NEON fast path: 8 pixels per inner iteration on ARM64.
    int16_t ky  = full_range ? 256 : 298;
    int16_t kr  = full_range ? 359 : 409;
    int16_t kgu = full_range ?  88 : 100;
    int16_t kgv = full_range ? 183 : 208;
    int16_t kb  = full_range ? 454 : 516;
    int16_t yoff = full_range ? 0 : 16;
    for (int row = 0; row < height; row++) {
        const uint8_t *yrow  = src->data[0] + row        * src->linesize[0];
        const uint8_t *uvrow = src->data[1] + (row >> 1) * src->linesize[1];
        uint8_t *drow = dst + row * dst_stride;
        int col = 0;
        for (; col + 7 < width; col += 8)
            grdp_nv12_to_bgra_neon_8(yrow, uvrow, drow, col, ky, kr, kgu, kgv, kb, yoff);
        // Scalar tail for widths not a multiple of 8.
        for (; col < width; col++) {
            int u = (int)uvrow[(col >> 1) * 2    ] - 128;
            int v = (int)uvrow[(col >> 1) * 2 + 1] - 128;
            grdp_bt601_pixel((int)yrow[col], u, v, full_range, drow + col*4);
        }
    }
#else
    for (int row = 0; row < height; row++) {
        const uint8_t *yrow  = src->data[0] + row        * src->linesize[0];
        const uint8_t *uvrow = src->data[1] + (row >> 1) * src->linesize[1];
        uint8_t *drow = dst + row * dst_stride;
        for (int col = 0; col < width; col++) {
            int u = (int)uvrow[(col >> 1) * 2    ] - 128;
            int v = (int)uvrow[(col >> 1) * 2 + 1] - 128;
            grdp_bt601_pixel((int)yrow[col], u, v, full_range, drow + col*4);
        }
    }
#endif
}

// grdp_sample_yuv samples the centre pixel of a planar YUV frame for
// diagnostics.  Returns raw byte values (not offset-adjusted).
static void grdp_sample_yuv(const AVFrame *f,
    uint8_t *y_out, uint8_t *u_out, uint8_t *v_out)
{
    int cx = f->width  / 2;
    int cy = f->height / 2;
    *y_out = f->data[0][ cy      * f->linesize[0] +  cx     ];
    *u_out = f->data[1][(cy / 2) * f->linesize[1] + (cx / 2)];
    *v_out = f->data[2][(cy / 2) * f->linesize[2] + (cx / 2)];
}

static void grdp_sws_set_src_range(struct SwsContext *sws, int full_range) {
    const int *inv_table, *table;
    int src_range, dst_range, brightness, contrast, saturation;
    if (sws_getColorspaceDetails(sws,
            (int **)&inv_table, &src_range,
            (int **)&table,     &dst_range,
            &brightness, &contrast, &saturation) >= 0) {
        sws_setColorspaceDetails(sws,
            inv_table, full_range,
            table,     dst_range,
            brightness, contrast, saturation);
    }
}
*/
import "C"

import (
	"bytes"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"time"
	"unsafe"
)

// useSwscaleForYUV420 controls whether YUV420P/YUVJ420P frames are converted
// to BGRA via swscale (SIMD-accelerated on x86_64) or via grdp_yuv420p_to_bgra
// (a portable scalar C loop).  swscale is preferred where it is both correct
// and fast; on ARM64 its non-accelerated yuv420p→bgra fallback ignores
// sws_setColorspaceDetails and produces a strong green cast, so we fall back
// to the hand-written loop there.
var useSwscaleForYUV420 = runtime.GOARCH != "arm64"

// useSwscaleForNV12 mirrors useSwscaleForYUV420 for NV12 (VideoToolbox HW
// output).  The same ARM64 swscale defect applies: the non-accelerated
// nv12→bgra path ignores sws_setColorspaceDetails, so a zero-filled NV12
// frame (e.g. produced by VideoToolbox during codec initialisation) is
// converted as full-range and renders as solid green instead of black.
// On x86_64, swscale's SSSE3/AVX2 NV12→BGRA path is both correct and fast.
var useSwscaleForNV12 = runtime.GOARCH != "arm64"

// avLogOnce ensures grdp_suppress_av_log is called only once per process.
var avLogOnce sync.Once

// avcFreezeThreshold is the duration of no decoded output after which the
// decoder is considered stalled and a server refresh is requested.
// Lowered from 30 s to 2 s so that stall detection fires before the
// application-level watchdog (~5 s), giving the decoder-level recovery
// (hard reset + IDR request) a chance to act first.
const avcFreezeThreshold = 2 * time.Second

// avcRefreshCooldown is the minimum interval between consecutive server
// refresh requests.  Lowered from 60 s to 5 s to allow rapid re-detection
// after a failed hard-reset attempt.
const avcRefreshCooldown = 5 * time.Second

// NOTE: HW→SW runtime fallback has been removed.  Empirically the SW
// decoder, once entered mid-session, frequently leaves the connection in a
// hung state from which it never recovers.  Instead, when the HW decoder
// hits an unrecoverable condition we mark the decoder `broken` and stop
// producing frames; the application-level watchdog (e.g. grdpsdl2's
// videoStallTimeout) will then reconnect, which restarts the whole RDP
// session and re-creates the decoder from scratch.

// hwHardErrorThreshold is the number of consecutive avcodec_send_packet
// failures on a non-IDR packet before we attempt a hard reset of the HW
// decoder.  Mirrors rdpyqt's _HW_ERROR_THRESHOLD (avc.py:484).
const hwHardErrorThreshold = 5

// keyframeWaitLimit is the maximum number of non-IDR packets we drop while
// waiting for a keyframe after a decoder reset.  If the server never sends a
// new IDR within this many packets we give up waiting and feed P-frames to the
// SW decoder anyway; FFmpeg's error-concealment keeps the session alive.
const keyframeWaitLimit = 150

// hwPostResetStuckThreshold is the maximum number of packets that can be
// delivered to a freshly hard-reset HW decoder (hwReady == false, i.e. after
// the first hardReset call) without producing any decoded frame before we
// consider the decoder permanently stuck and either retry the reset or mark it
// broken.  At ~30 fps this corresponds to roughly 2 seconds of no output.
const hwPostResetStuckThreshold = 60

// hwMaxRecoveries is the maximum number of hard resets attempted before the
// decoder is marked broken and the application-level watchdog reconnects.
// Each attempt waits up to hwPostResetStuckThreshold packets (~2 s at 30 fps)
// for an IDR before retrying, so the total wait before reconnect is roughly
// hwMaxRecoveries * 2 s.
//
// Empirically, when a Windows RDPGFX server is mid video-stream and the
// VideoToolbox HW decoder gets stuck, neither SendRefreshRect nor the
// SuppressOutput off→on toggle reliably elicits a fresh IDR — the server
// keeps emitting AVC444 chroma-upgrade (LC=2) packets while withholding any
// new luma/IDR.  In that state, additional hard resets cannot recover the
// decoder; they merely prolong the visible video freeze.  A full RDP
// reconnect is the only known-good remedy, so we cap the retry budget low
// enough that the user sees a brief freeze and then a clean reconnect.
// Mirrors rdpyqt avc.py _hw_reset_count >= 3 fallback threshold.
const hwMaxRecoveries = 3

type ffmpegDecoder struct {
	codecCtx  *C.AVCodecContext
	packet    *C.AVPacket
	frame     *C.AVFrame
	swFrame   *C.AVFrame
	swsCtx    *C.struct_SwsContext
	useHW     bool
	hwPixFmt  C.enum_AVPixelFormat
	lastW     C.int
	lastH     C.int
	lastFmt   C.enum_AVPixelFormat
	lastFullRange C.int // tracks fullRange used when swsCtx was last configured
	stallCycles        int       // consecutive HW stall→nudge cycles without any successful decode in between
	lastSuccessTime    time.Time // wall-clock time of the last successfully decoded frame
	lastRefreshTime    time.Time // wall-clock time of the last server-refresh request
	needsKeyFrame     bool // drop packets until an IDR/SPS is received
	keyframeWaitCount int  // P-frames dropped so far while needsKeyFrame=true
	hwReady           bool // HW decoder has produced at least one frame
	hwSentCount       int  // packets sent to HW decoder (for diagnostics)
	hwErrorCount      int  // consecutive avcodec_send_packet hard errors on HW
	hwRecoveries      int  // number of HW hard-reset (recreate) attempts made
	swFrameCount      int  // frames decoded by SW decoder (for diagnostics)
	broken            bool // decoder is unrecoverable; stop producing frames so the app reconnects

	// outRing holds two recyclable BGRA destination buffers.  convertFrame
	// rotates between them so each Decode() avoids allocating a fresh
	// width*height*4 buffer (≈8MB at 1920×1080 → ≈240MB/s of GC garbage at
	// 30fps).  Two slots is sufficient because emitBitmap is called
	// synchronously from the rdpgfx PDU loop and always finishes (the
	// caller has copied the data into its backing image) before the next
	// Decode runs.  outRingIdx selects the slot to use *next*.
	outRing    [2][]byte
	outRingIdx int

	// SPS/PPS cache (Annex B framing, including start code).  Captured by
	// scanning every Annex B stream we feed to the decoder.  After a hard
	// reset of the HW decoder we prepend these to the next IDR so the fresh
	// AVCodecContext has the parameter sets it needs to decode bare IDRs
	// (Windows RDPGFX servers often omit SPS/PPS from IDR packets after the
	// first one).  Mirrors rdpyqt avc.py:_parse_and_cache_nals.
	spsNAL              []byte
	ppsNAL              []byte
	prependSPSNextIDR   bool

	// wantsServerRefresh asks the GfxHandler to send a SendRefreshRect to
	// the server to nudge a fresh IDR.  Distinct from needsKeyFrame:
	// needsKeyFrame additionally *drops* incoming P-frames until an IDR
	// arrives.  wantsServerRefresh does NOT drop packets — we keep feeding
	// the decoder so VideoToolbox can recover naturally from the next IDR
	// the server happens to send (rdpyqt avc.py:140-166).
	wantsServerRefresh bool

	// postResetPackets counts packets delivered to the decoder while
	// hwReady == false *after* at least one hard reset (hwRecoveries > 0).
	// If this exceeds hwPostResetStuckThreshold without a decoded frame we
	// retry the hard reset or mark the decoder broken.
	postResetPackets int
}

func newH264Decoder() h264Decoder {
	// Suppress FFmpeg stderr output (e.g. "[h264 @ ...] sps_id out of range").
	// grdp emits its own slog messages for H.264 recovery events.
	avLogOnce.Do(func() { C.grdp_suppress_av_log() })

	codec := C.avcodec_find_decoder(C.AV_CODEC_ID_H264)
	if codec == nil {
		slog.Warn("H.264: codec not found in FFmpeg")
		return nil
	}

	codecCtx := C.avcodec_alloc_context3(codec)
	if codecCtx == nil {
		return nil
	}

	d := &ffmpegDecoder{
		codecCtx: codecCtx,
		hwPixFmt: C.AV_PIX_FMT_NONE,
		lastFmt:  C.AV_PIX_FMT_NONE,
	}

	// Always enable LOW_DELAY: RDP H.264 streams are transmitted in display
	// order with no B-frame reordering, so the default reorder buffer adds
	// no value and (especially on VideoToolbox) makes the decoder appear
	// stalled between IDRs.
	C.grdp_set_low_delay(codecCtx)

	// Probe available hardware acceleration backends.
	hwType := C.av_hwdevice_iterate_types(C.AV_HWDEVICE_TYPE_NONE)
	for hwType != C.AV_HWDEVICE_TYPE_NONE {
		var devCtx *C.AVBufferRef
		if C.av_hwdevice_ctx_create(&devCtx, hwType, nil, nil, 0) == 0 {
			// Find the HW pixel format for this device type.
			hwPixFmt := C.enum_AVPixelFormat(C.AV_PIX_FMT_NONE)
			for i := C.int(0); ; i++ {
				cfg := C.avcodec_get_hw_config(codec, i)
				if cfg == nil {
					break
				}
				if cfg.device_type == hwType &&
					(cfg.methods&C.AV_CODEC_HW_CONFIG_METHOD_HW_DEVICE_CTX) != 0 {
					hwPixFmt = cfg.pix_fmt
					break
				}
			}

			if hwPixFmt != C.AV_PIX_FMT_NONE {
				codecCtx.hw_device_ctx = C.av_buffer_ref(devCtx)
				C.grdp_set_hw_pix_fmt(codecCtx, hwPixFmt)
				C.grdp_set_get_format(codecCtx)
				d.useHW = true
				d.hwPixFmt = hwPixFmt
				name := C.av_hwdevice_get_type_name(hwType)
				slog.Debug("H.264: hardware acceleration enabled", "type", C.GoString(name))
			}
			C.av_buffer_unref(&devCtx)
			if d.useHW {
				break
			}
		}
		hwType = C.av_hwdevice_iterate_types(hwType)
	}

	if !d.useHW {
		slog.Debug("H.264: using software decoding")
	}

	if C.avcodec_open2(codecCtx, codec, nil) < 0 {
		C.avcodec_free_context(&d.codecCtx)
		return nil
	}

	d.packet = C.av_packet_alloc()
	d.frame = C.av_frame_alloc()
	d.swFrame = C.av_frame_alloc()
	if d.packet == nil || d.frame == nil || d.swFrame == nil {
		d.Close()
		return nil
	}

	runtime.SetFinalizer(d, func(dec *ffmpegDecoder) { dec.Close() })
	return d
}

func (d *ffmpegDecoder) NeedsKeyframe() bool {
	return d.needsKeyFrame || d.wantsServerRefresh
}

func (d *ffmpegDecoder) IsBroken() bool {
	return d.broken
}

// HardResetCount returns the number of hard resets performed so far.
// GfxHandler uses this to detect a new reset and clear its keyframe rate-limit.
func (d *ffmpegDecoder) HardResetCount() int {
	return d.hwRecoveries
}

func (d *ffmpegDecoder) Decode(h264Data []byte) (*h264Frame, error) {
	if len(h264Data) == 0 {
		return nil, nil
	}
	if d.broken {
		// HW decoder is unrecoverable.  Stop feeding packets so no frames
		// are produced; the application-level watchdog will reconnect.
		return nil, nil
	}

	// After a decoder reset we must resync with a fresh IDR from the server.
	// Priming with a cached IDR does NOT work: P-frames mid-GOP expect a DPB
	// containing several preceding reference frames that we no longer have.
	// Feeding only the cached IDR causes the SW decoder to output zero-filled
	// frames (all Y=U=V=0) which render as solid green.  The correct recovery
	// is to wait for the server to begin a new GOP.  maybeRequestKeyframe()
	// calls SendRefreshRect every 3 s to prompt the server.
	// If the server never sends an IDR within keyframeWaitLimit packets we
	// fall back to SW error-concealment so the session does not hang.
	// FFmpeg's "[h264 @ ...] sps_id out of range" errors are suppressed at
	// the av_log level (AV_LOG_FATAL) set in newH264Decoder; grdp emits its
	// own slog warning instead.
	// Single pass over the Annex B stream: detect IDR/SPS NAL presence and
	// (re)cache SPS/PPS in one walk.  Replaces three separate linear scans
	// (h264ContainsKeyFrame ×2 + scanAndCacheParamSets).
	scan := scanH264Packet(h264Data)
	if scan.spsEnd > scan.spsStart {
		nal := h264Data[scan.spsStart:scan.spsEnd]
		if !bytes.Equal(nal, d.spsNAL) {
			d.spsNAL = append(d.spsNAL[:0], nal...)
		}
	}
	if scan.ppsEnd > scan.ppsStart {
		nal := h264Data[scan.ppsStart:scan.ppsEnd]
		if !bytes.Equal(nal, d.ppsNAL) {
			d.ppsNAL = append(d.ppsNAL[:0], nal...)
		}
	}

	if d.needsKeyFrame {
		if !scan.hasKeyFrame {
			d.keyframeWaitCount++
			if d.keyframeWaitCount >= keyframeWaitLimit {
				slog.Debug("H.264: no IDR received, proceeding without keyframe",
					"waited", d.keyframeWaitCount)
				d.needsKeyFrame = false
				d.keyframeWaitCount = 0
				// fall through and attempt SW error-concealment decode
			} else {
				return nil, nil // drop P-frames while waiting
			}
		} else {
			d.needsKeyFrame = false
			d.keyframeWaitCount = 0
		}
	}

	// Time-based stall detection.  Only fires once the decoder has proven it
	// can produce frames (hwReady=true for HW, or lastSuccessTime set for SW).
	// When hwReady=false the decoder is in a post-reset window handled
	// exclusively by the postResetPackets counter below; firing the time-based
	// stall here would trigger extra hard resets that bypass hwMaxRecoveries.
	if !d.lastSuccessTime.IsZero() && (!d.useHW || d.hwReady) {
		frozenFor := time.Since(d.lastSuccessTime)
		if frozenFor >= avcFreezeThreshold {
			if time.Since(d.lastRefreshTime) >= avcRefreshCooldown {
				d.lastRefreshTime = time.Now()
				if d.useHW {
					d.stallCycles++
					// If nudging has not produced any frame across several cycles,
					// VT is stuck in an unrecoverable state (often after a stream
					// parameter change where the cached SPS no longer matches).
					// flush_buffers cannot recover this — only a full CodecContext
					// recreate works.  Mirrors rdpyqt avc.py:_hard_reset escalation.
					// hwStuckCycles=1: skip the nudge phase and go straight to hard
					// reset; nudging rarely causes the server to send an IDR during
					// active video streaming.
					const hwStuckCycles = 1
					if d.stallCycles >= hwStuckCycles {
						d.stallCycles = 0
						if d.hwRecoveries >= hwMaxRecoveries {
							// Budget exhausted — mark broken immediately instead of
							// doing another futile reset.  The postResetPackets path
							// (below) should normally handle this, but guard here too
							// in case the stall fires concurrently with a post-reset.
							slog.Warn("H.264: HW decoder unrecoverable after max resets (stall), marking broken",
								"hwRecoveries", d.hwRecoveries, "frozenFor", frozenFor)
							d.broken = true
							d.wantsServerRefresh = false
							return nil, nil
						}
						slog.Debug("H.264: HW decoder stuck, performing hard reset",
							"frozenFor", frozenFor)
						d.hardResetHW()
					} else {
						slog.Debug("H.264: HW decoder stalled, nudging server for IDR (no drop, no flush)",
							"frozenFor", frozenFor, "cycle", d.stallCycles)
					}
					d.wantsServerRefresh = true
				} else {
					slog.Debug("H.264: SW decoder stalled, flushing", "frozenFor", frozenFor)
					C.avcodec_flush_buffers(d.codecCtx)
				}
			}
		}
	}

	// If we just recreated the HW decoder via hardReset(), prepend cached
	// SPS+PPS to the first IDR we send through.  The fresh codec context
	// has no parameter sets and Windows RDPGFX servers send bare IDRs
	// (without SPS/PPS) after the first IDR of the session.
	feedData := h264Data
	if d.prependSPSNextIDR && d.useHW &&
		scan.hasKeyFrame &&
		len(d.spsNAL) > 0 && len(d.ppsNAL) > 0 {
		buf := make([]byte, 0, len(d.spsNAL)+len(d.ppsNAL)+len(h264Data))
		buf = append(buf, d.spsNAL...)
		buf = append(buf, d.ppsNAL...)
		buf = append(buf, h264Data...)
		feedData = buf
		d.prependSPSNextIDR = false
		slog.Debug("H.264: prepending cached SPS+PPS to IDR after hard reset",
			"sps", len(d.spsNAL), "pps", len(d.ppsNAL), "idr", len(h264Data))
	}

	// Pass the Go slice's backing array directly to avcodec_send_packet
	// instead of allocating + copying via C.CBytes for every packet.
	// FFmpeg copies the buffer internally for non-refcounted packets, so the
	// memory only needs to remain valid for the duration of the C call —
	// runtime.KeepAlive guarantees this.
	d.packet.data = (*C.uint8_t)(unsafe.Pointer(&feedData[0]))
	d.packet.size = C.int(len(feedData))

	// Count packets sent to HW decoder (for init timeout tracking).
	if d.useHW {
		d.hwSentCount++
	}

	ret := C.avcodec_send_packet(d.codecCtx, d.packet)
	// Make sure the Go-managed feedData backing array is not collected or
	// moved while FFmpeg is reading from it inside the C call above.
	runtime.KeepAlive(feedData)
	// Drop the Go pointer from the AVPacket immediately so a subsequent
	// avcodec_* call can't dereference stale memory.
	d.packet.data = nil
	d.packet.size = 0
	if ret < 0 {
		if d.useHW {
			// After a hard reset the freshly-recreated decoder has no SPS/PPS
			// until the server's next IDR is fed to it (with prependSPSNextIDR).
			// avcodec_send_packet on intervening P-frames is *expected* to fail
			// and must NOT trigger another hard reset — that would just repeat
			// the cycle.  Mirrors rdpyqt avc.py where decode() returning
			// silently for non-IDR packets does not increment _hw_error_count.
			// We ask the GfxHandler to nudge the server (via wantsServerRefresh)
			// so an IDR arrives soon.
			if !d.hwReady {
				d.wantsServerRefresh = true
				if d.hwRecoveries > 0 {
					// We are in the post-hard-reset window.  Count packets so
					// that if the server never delivers a usable IDR we can
					// detect the permanent-freeze state and retry / give up.
					d.postResetPackets++
					if d.postResetPackets >= hwPostResetStuckThreshold {
						if d.hwRecoveries < hwMaxRecoveries {
							slog.Debug("H.264: HW decoder stuck after hard reset, retrying",
								"postResetPackets", d.postResetPackets,
								"attempt", d.hwRecoveries+1)
							d.hardResetHW()
						} else {
							slog.Warn("H.264: HW decoder unrecoverable after max resets, marking broken",
								"hwRecoveries", d.hwRecoveries)
							d.broken = true
							d.wantsServerRefresh = false
						}
					}
				}
				return nil, nil
			}
			// VideoToolbox hard error.  flush_buffers cannot recover this;
			// only a full CodecContext recreate can (proven by rdpyqt's
			// extensive macOS testing — see avc.py:214-260).  Keep retrying
			// hard resets; if HW truly cannot recover, hardResetHW will set
			// d.broken so the application-level watchdog reconnects.
			d.hwErrorCount++
			slog.Debug("H.264: HW avcodec_send_packet failed",
				"err", int(ret), "hwErrorCount", d.hwErrorCount)
			if d.hwErrorCount >= hwHardErrorThreshold {
				slog.Debug("H.264: HW decoder hard error, recreating context",
					"hwErrorCount", d.hwErrorCount,
					"attempt", d.hwRecoveries+1)
				d.hardResetHW()
			}
			return nil, nil
		}
		// SW decoder: flush and wait for a new IDR.
		slog.Debug("H.264: avcodec_send_packet failed, flushing decoder to recover",
			"err", int(ret))
		C.avcodec_flush_buffers(d.codecCtx)
		d.needsKeyFrame = true
		d.keyframeWaitCount = 0
		return nil, nil
	}
	d.hwErrorCount = 0

	// Receive decoded frame(s); keep the last one.
	var result *h264Frame
	for {
		ret = C.avcodec_receive_frame(d.codecCtx, d.frame)
		if ret < 0 {
			break // EAGAIN (need more input) or EOF
		}
		f, err := d.convertFrame()
		C.av_frame_unref(d.frame)
		if err != nil {
			return nil, err
		}
		result = f
	}

	if result != nil {
		d.lastSuccessTime = time.Now()
		d.stallCycles = 0
		d.wantsServerRefresh = false
		if d.useHW {
			if !d.hwReady {
				slog.Debug("H.264: HW decoder produced first frame",
					"hwSentCount", d.hwSentCount)
			}
			d.hwReady = true // HW has proven it can produce frames
			d.hwRecoveries = 0 // successful decode, reset recovery counter
		}
	} else {
		// No frame produced.  Stall detection is now time-based (see above);
		// we no longer increment a packet counter here.  Only log for HW after
		// the decoder has proven it works (hwReady) so normal B-frame
		// reordering delays during startup are not noisy.
		if d.useHW && d.hwReady {
			slog.Debug("H.264: HW null frame", "frozenFor", time.Since(d.lastSuccessTime),
				"hwSentCount", d.hwSentCount)
		}
	}
	return result, nil
}

func (d *ffmpegDecoder) convertFrame() (*h264Frame, error) {
	srcFrame := d.frame

	// Transfer from GPU to CPU memory if using hardware acceleration.
	if d.useHW && d.frame.format == C.int(d.hwPixFmt) {
		ret := C.av_hwframe_transfer_data(d.swFrame, d.frame, 0)
		if ret < 0 {
			return nil, fmt.Errorf("av_hwframe_transfer_data: error %d", int(ret))
		}
		srcFrame = d.swFrame
	}

	w := srcFrame.width
	h := srcFrame.height
	srcFmt := C.enum_AVPixelFormat(srcFrame.format)

	outSize := int(w) * int(h) * 4
	// Borrow the next ring buffer instead of allocating fresh.  At 1920×1080
	// this avoids an 8MB allocation every frame.
	out := d.outRing[d.outRingIdx]
	if cap(out) < outSize {
		out = make([]byte, outSize)
	} else {
		out = out[:outSize]
	}
	d.outRing[d.outRingIdx] = out
	d.outRingIdx ^= 1

	// For planar YUV420P (both limited- and full-range variants), use our own
	// BT.601 conversion instead of swscale on ARM64.  swscale has no
	// accelerated colorspace-conversion path for yuv420p→bgra on ARM64 and
	// its non-accelerated fallback ignores sws_setColorspaceDetails,
	// producing a strong green cast.  On x86_64 swscale is both correct and
	// significantly faster (SIMD-accelerated), so we route through swscale
	// there and only fall back to the hand-written loop on ARM64.
	if (srcFmt == C.AV_PIX_FMT_YUV420P || srcFmt == C.AV_PIX_FMT_YUVJ420P) && !useSwscaleForYUV420 {
		fullRange := C.int(0)
		if srcFmt == C.AV_PIX_FMT_YUVJ420P || srcFrame.color_range == 2 {
			fullRange = 1
		}
		// Log the centre-pixel YUV values for the first few SW frames so we
		// can distinguish H.264 decode corruption from colour-conversion bugs.
		if !d.useHW && d.swFrameCount < 3 {
			var sy, su, sv C.uint8_t
			C.grdp_sample_yuv(srcFrame, &sy, &su, &sv)
			slog.Info("H.264: SW frame sample",
				"frame", d.swFrameCount,
				"fmt", int(srcFmt),
				"fullRange", int(fullRange),
				"Y", int(sy), "U", int(su), "V", int(sv),
				"w", int(w), "h", int(h))
			d.swFrameCount++
		}
		C.grdp_yuv420p_to_bgra(srcFrame,
			(*C.uint8_t)(unsafe.Pointer(&out[0])), C.int(w*4), fullRange)
		if srcFrame == d.swFrame {
			C.av_frame_unref(d.swFrame)
		}
		return &h264Frame{Data: out, Width: int(w), Height: int(h)}, nil
	}

	// For NV12 (VideoToolbox HW transfer output) on ARM64, bypass swscale for
	// the same reason as YUV420P: the non-accelerated ARM64 path ignores
	// sws_setColorspaceDetails and produces a green cast on zero-filled frames.
	if srcFmt == C.AV_PIX_FMT_NV12 && !useSwscaleForNV12 {
		fullRange := C.int(0)
		if srcFrame.color_range == 2 { // AVCOL_RANGE_JPEG
			fullRange = 1
		}
		C.grdp_nv12_to_bgra(srcFrame,
			(*C.uint8_t)(unsafe.Pointer(&out[0])), C.int(w*4), fullRange)
		if srcFrame == d.swFrame {
			C.av_frame_unref(d.swFrame)
		}
		return &h264Frame{Data: out, Width: int(w), Height: int(h)}, nil
	}

	// For other formats (e.g. NV12 from VideoToolbox HW transfer), use swscale.
	swsFmt := C.grdp_yuvj_to_yuv(srcFmt)
	fullRange := C.grdp_is_full_range_fmt(srcFmt)
	if fullRange == 0 && srcFrame.color_range == 2 { // AVCOL_RANGE_JPEG
		fullRange = 1
	}

	if w != d.lastW || h != d.lastH || srcFmt != d.lastFmt || fullRange != d.lastFullRange {
		if d.swsCtx != nil {
			C.sws_freeContext(d.swsCtx)
		}
		d.swsCtx = C.sws_getContext(
			w, h, swsFmt,
			w, h, C.AV_PIX_FMT_BGRA,
			C.SWS_BILINEAR, nil, nil, nil,
		)
		if d.swsCtx == nil {
			return nil, fmt.Errorf("sws_getContext failed for %dx%d fmt=%d", w, h, srcFmt)
		}
		C.grdp_sws_set_src_range(d.swsCtx, fullRange)
		d.lastW = w
		d.lastH = h
		d.lastFmt = srcFmt
		d.lastFullRange = fullRange
	}

	C.grdp_frame_to_bgra(d.swsCtx, srcFrame,
		(*C.uint8_t)(unsafe.Pointer(&out[0])), C.int(w*4))

	if srcFrame == d.swFrame {
		C.av_frame_unref(d.swFrame)
	}

	return &h264Frame{
		Data:   out,
		Width:  int(w),
		Height: int(h),
	}, nil
}

// scanResult holds the IDR-presence flag and SPS/PPS NAL boundaries (offsets
// into the original packet, including Annex B start code) discovered during a
// single linear walk of an Annex B H.264 packet.  Use scanH264Packet to
// produce one.
type scanResult struct {
	hasKeyFrame                          bool
	spsStart, spsEnd, ppsStart, ppsEnd int
}

// scanH264Packet walks an Annex B H.264 packet exactly once, returning
// whether it contains any IDR slice (NAL type 5) or SPS (NAL type 7) NAL
// unit and recording the byte ranges for the most recent SPS/PPS NALs found
// (start offset includes the Annex B start code).  Replaces what used to be
// three separate linear scans (h264ContainsKeyFrame ×2 + scanAndCacheParamSets)
// performed per-packet.
// scanH264Packet walks an Annex B H.264 packet exactly once, returning
// whether it contains any IDR slice (NAL type 5) or SPS (NAL type 7) NAL
// unit and recording the byte ranges for the most recent SPS/PPS NALs found
// (start offset includes the Annex B start code).  Replaces what used to be
// three separate linear scans (h264ContainsKeyFrame ×2 + scanAndCacheParamSets)
// performed per-packet.
//
// Uses bytes.Index to locate the canonical 3-byte start code (0x000001),
// promoting it to the 4-byte form when preceded by a zero.  bytes.Index is
// implemented in optimized assembly on the major Go platforms, so this
// out-performs a hand-rolled byte loop especially for the long IDR packets
// that dominate the H.264 hot path.
func scanH264Packet(data []byte) scanResult {
	var r scanResult
	startCode := []byte{0, 0, 1}
	pos := 0
	// nalStart points to the byte just past the previous NAL header byte
	// (i.e. into the NAL payload), used as the lower bound when searching
	// for the *next* start code so we never re-find the current one.
	for pos < len(data) {
		off := bytes.Index(data[pos:], startCode)
		if off < 0 {
			break
		}
		i := pos + off
		scLen := 3
		if i > 0 && data[i-1] == 0 {
			i--
			scLen = 4
		}
		if i+scLen >= len(data) {
			break
		}
		nalType := data[i+scLen] & 0x1F
		if nalType == 5 || nalType == 7 {
			r.hasKeyFrame = true
		}
		if nalType == 7 || nalType == 8 {
			// Locate the end of this NAL: search for the next 0x000001
			// from just past the NAL header byte.
			searchFrom := i + scLen + 1
			j := len(data)
			if searchFrom < len(data) {
				if next := bytes.Index(data[searchFrom:], startCode); next >= 0 {
					j = searchFrom + next
					// If preceded by a zero, that zero belongs to the
					// next start code (4-byte form).
					if j > 0 && data[j-1] == 0 {
						j--
					}
				}
			}
			if nalType == 7 {
				r.spsStart, r.spsEnd = i, j
			} else {
				r.ppsStart, r.ppsEnd = i, j
			}
			pos = j
			continue
		}
		pos = i + scLen + 1
	}
	return r
}

// hardResetHW destroys the AVCodecContext and recreates a fresh HW-accelerated
// one.  Used when avcodec_send_packet enters a persistent error state on
// VideoToolbox that flush_buffers cannot recover from.  The cached SPS+PPS
// are scheduled to be prepended to the next IDR so the new context has the
// codec parameters it needs.  If recreation fails (no HW backend, alloc/open
// error) the decoder is marked broken; the application-level watchdog will
// then reconnect, which restarts the whole RDP session.  Mirrors rdpyqt
// avc.py:_hard_reset, but without the SW fallback path.
func (d *ffmpegDecoder) hardResetHW() {
	d.hwRecoveries++

	// Try to find a HW backend again.
	codec := C.avcodec_find_decoder(C.AV_CODEC_ID_H264)
	if codec == nil {
		slog.Warn("H.264: hardResetHW: codec not found, marking decoder broken")
		d.broken = true
		return
	}
	if d.codecCtx != nil {
		C.avcodec_free_context(&d.codecCtx)
	}
	d.codecCtx = C.avcodec_alloc_context3(codec)
	if d.codecCtx == nil {
		slog.Warn("H.264: hardResetHW: avcodec_alloc_context3 failed, marking decoder broken")
		d.broken = true
		return
	}
	C.grdp_set_low_delay(d.codecCtx)

	// Re-attach a HW device of the previously-used type if possible.
	hwOK := false
	hwType := C.av_hwdevice_iterate_types(C.AV_HWDEVICE_TYPE_NONE)
	for hwType != C.AV_HWDEVICE_TYPE_NONE && !hwOK {
		var devCtx *C.AVBufferRef
		if C.av_hwdevice_ctx_create(&devCtx, hwType, nil, nil, 0) == 0 {
			hwPixFmt := C.enum_AVPixelFormat(C.AV_PIX_FMT_NONE)
			for i := C.int(0); ; i++ {
				cfg := C.avcodec_get_hw_config(codec, i)
				if cfg == nil {
					break
				}
				if cfg.device_type == hwType &&
					(cfg.methods&C.AV_CODEC_HW_CONFIG_METHOD_HW_DEVICE_CTX) != 0 {
					hwPixFmt = cfg.pix_fmt
					break
				}
			}
			if hwPixFmt != C.AV_PIX_FMT_NONE {
				d.codecCtx.hw_device_ctx = C.av_buffer_ref(devCtx)
				C.grdp_set_hw_pix_fmt(d.codecCtx, hwPixFmt)
				C.grdp_set_get_format(d.codecCtx)
				d.hwPixFmt = hwPixFmt
				hwOK = true
			}
			C.av_buffer_unref(&devCtx)
		}
		hwType = C.av_hwdevice_iterate_types(hwType)
	}
	if !hwOK {
		// HW backend unavailable on this reset attempt.  Per user
		// requirement, do NOT fall back to a software decoder (the SW
		// path produced more problems than it solved); instead mark the
		// decoder broken and let the application-level watchdog reconnect.
		slog.Warn("H.264: hardResetHW: no HW backend available, marking decoder broken")
		d.broken = true
		return
	}

	if C.avcodec_open2(d.codecCtx, codec, nil) < 0 {
		slog.Warn("H.264: hardResetHW: avcodec_open2 failed, marking decoder broken")
		d.broken = true
		return
	}

	d.useHW = true
	d.lastW = 0
	d.lastH = 0
	d.lastFmt = C.AV_PIX_FMT_NONE
	// Do NOT set needsKeyFrame=true here.  Dropping P-frames for
	// keyframeWaitLimit (150) packets while the server may not send a
	// fresh IDR for several seconds wastes the wait, and after the wait
	// expires we feed P-frames to a fresh decoder that has no SPS/PPS,
	// which fails 5x and triggers another hard reset.  rdpyqt does NOT
	// drop packets after a hard reset; it just asks the server for a
	// refresh and lets the decoder silently fail on intervening P-frames
	// until the next IDR arrives (avc.py:140-260).
	d.needsKeyFrame = false
	d.keyframeWaitCount = 0
	// hwReady gates the HW-only stall/post-reset logic in Decode().
	// Mark it not-ready so the first frame proves itself.
	d.hwReady = false
	d.hwSentCount = 0
	d.hwErrorCount = 0
	d.postResetPackets = 0
	// Ask the GfxHandler to nudge the server for a fresh IDR.  Until that
	// IDR arrives, send_packet on P-frames is expected to fail silently
	// (handled in Decode where !d.hwReady suppresses the hard-reset cascade).
	d.wantsServerRefresh = true
	d.stallCycles = 0
	// Reset the time-based stall baseline so the next stall check measures
	// freshly from this reset, not from the original stall ~2 s ago.  Without
	// this, the very first packet after the avcRefreshCooldown window
	// elapses re-triggers the time-based hard-reset path (frozenFor ≥ 2 s)
	// even though we have just recreated the decoder.
	d.lastSuccessTime = time.Now()
	d.prependSPSNextIDR = len(d.spsNAL) > 0 && len(d.ppsNAL) > 0
	slog.Debug("H.264: HW decoder hard-reset complete",
		"recovery", d.hwRecoveries,
		"spsCached", len(d.spsNAL), "ppsCached", len(d.ppsNAL))
}

func (d *ffmpegDecoder) Close() {
	if d.swsCtx != nil {
		C.sws_freeContext(d.swsCtx)
		d.swsCtx = nil
	}
	if d.frame != nil {
		C.av_frame_free(&d.frame)
	}
	if d.swFrame != nil {
		C.av_frame_free(&d.swFrame)
	}
	if d.packet != nil {
		C.av_packet_free(&d.packet)
	}
	if d.codecCtx != nil {
		C.avcodec_free_context(&d.codecCtx)
	}
}
