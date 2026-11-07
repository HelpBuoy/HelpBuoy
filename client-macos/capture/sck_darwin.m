#import <AppKit/AppKit.h>
#import <CoreMedia/CoreMedia.h>
#import <CoreVideo/CoreVideo.h>
#import <ScreenCaptureKit/ScreenCaptureKit.h>
#include <os/lock.h>
#include <string.h>

#include "sck.h"

static void seterr(char *err, int errlen, NSString *msg) {
	if (errlen <= 0) {
		return;
	}
	const char *s = msg ? msg.UTF8String : "unknown error";
	strncpy(err, s ? s : "unknown error", errlen - 1);
	err[errlen - 1] = 0;
}

int hb_displays(hb_display *out, int max) {
	CGDirectDisplayID ids[32];
	uint32_t n = 0;
	if (CGGetActiveDisplayList(32, ids, &n) != kCGErrorSuccess) {
		return -1;
	}
	CGDirectDisplayID main = CGMainDisplayID();
	int k = 0;
	for (uint32_t i = 0; i < n && k < max; i++) {
		if (CGDisplayMirrorsDisplay(ids[i]) != kCGNullDirectDisplay) {
			continue; // a mirror shows another display's pixels
		}
		CGRect r = CGDisplayBounds(ids[i]);
		out[k].id = ids[i];
		out[k].x = r.origin.x;
		out[k].y = r.origin.y;
		out[k].w = r.size.width;
		out[k].h = r.size.height;
		out[k].primary = ids[i] == main;
		k++;
	}
	return k;
}

// HBOutput receives the stream's frames on its queue and keeps the newest.
API_AVAILABLE(macos(12.3))
@interface HBOutput : NSObject <SCStreamOutput, SCStreamDelegate>
- (int)copyTo:(uint8_t *)dst stride:(int)stride width:(int)w height:(int)h seq:(uint64_t *)seq err:(char *)err len:(int)errlen;
@end

@implementation HBOutput {
	os_unfair_lock _lock;
	CVPixelBufferRef _frame; // retained
	uint64_t _seq;
	BOOL _stopped;
	NSString *_error;
}

- (instancetype)init {
	if ((self = [super init])) {
		_lock = OS_UNFAIR_LOCK_INIT;
	}
	return self;
}

- (void)dealloc {
	if (_frame) {
		CVPixelBufferRelease(_frame);
	}
}

- (void)stream:(SCStream *)stream didOutputSampleBuffer:(CMSampleBufferRef)sb ofType:(SCStreamOutputType)type {
	if (type != SCStreamOutputTypeScreen || !CMSampleBufferIsValid(sb)) {
		return;
	}
	// Frames without changes (idle, blank, suspended) carry no new pixels.
	CFArrayRef atts = CMSampleBufferGetSampleAttachmentsArray(sb, false);
	if (atts && CFArrayGetCount(atts) > 0) {
		NSDictionary *a = (__bridge NSDictionary *)CFArrayGetValueAtIndex(atts, 0);
		NSNumber *status = a[SCStreamFrameInfoStatus];
		if (status && status.integerValue != SCFrameStatusComplete) {
			return;
		}
	}
	CVPixelBufferRef pb = CMSampleBufferGetImageBuffer(sb);
	if (!pb) {
		return;
	}
	CVPixelBufferRetain(pb);
	os_unfair_lock_lock(&_lock);
	CVPixelBufferRef old = _frame;
	_frame = pb;
	_seq++;
	os_unfair_lock_unlock(&_lock);
	if (old) {
		CVPixelBufferRelease(old);
	}
}

- (void)stream:(SCStream *)stream didStopWithError:(NSError *)error {
	os_unfair_lock_lock(&_lock);
	_stopped = YES;
	_error = error.localizedDescription;
	os_unfair_lock_unlock(&_lock);
}

- (int)copyTo:(uint8_t *)dst stride:(int)stride width:(int)w height:(int)h seq:(uint64_t *)seq err:(char *)err len:(int)errlen {
	os_unfair_lock_lock(&_lock);
	if (_stopped) {
		NSString *e = _error ?: @"the screen capture stopped";
		os_unfair_lock_unlock(&_lock);
		seterr(err, errlen, e);
		return -1;
	}
	if (!_frame || _seq == *seq) {
		os_unfair_lock_unlock(&_lock);
		return 0;
	}
	CVPixelBufferRef pb = CVPixelBufferRetain(_frame);
	uint64_t s = _seq;
	os_unfair_lock_unlock(&_lock);

	if (CVPixelBufferLockBaseAddress(pb, kCVPixelBufferLock_ReadOnly) != kCVReturnSuccess) {
		CVPixelBufferRelease(pb);
		return 0;
	}
	const uint8_t *src = CVPixelBufferGetBaseAddress(pb);
	size_t sstride = CVPixelBufferGetBytesPerRow(pb);
	int cw = MIN(w, (int)CVPixelBufferGetWidth(pb));
	int ch = MIN(h, (int)CVPixelBufferGetHeight(pb));
	for (int y = 0; y < ch; y++) {
		memcpy(dst + (size_t)y * stride, src + (size_t)y * sstride, (size_t)cw * 4);
	}
	CVPixelBufferUnlockBaseAddress(pb, kCVPixelBufferLock_ReadOnly);
	CVPixelBufferRelease(pb);
	*seq = s;
	return 1;
}
@end

struct hb_stream {
	void *stream; // SCStream, retained
	void *output; // HBOutput, retained
};

static const int64_t waitSeconds = 10;

hb_stream *hb_stream_start(uint32_t display, int width, int height, char *err, int errlen) {
	if (@available(macOS 12.3, *)) {
		@autoreleasepool {
			dispatch_semaphore_t sem = dispatch_semaphore_create(0);
			__block SCShareableContent *content = nil;
			__block NSError *cerr = nil;
			[SCShareableContent getShareableContentExcludingDesktopWindows:NO
			                                           onScreenWindowsOnly:YES
			                                             completionHandler:^(SCShareableContent *c, NSError *e) {
				                                             content = c;
				                                             cerr = e;
				                                             dispatch_semaphore_signal(sem);
			                                             }];
			if (dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, waitSeconds * NSEC_PER_SEC))) {
				seterr(err, errlen, @"the screen did not answer");
				return NULL;
			}
			if (!content) {
				seterr(err, errlen, cerr.localizedDescription ?: @"the screen can't be recorded");
				return NULL;
			}
			SCDisplay *d = nil;
			for (SCDisplay *x in content.displays) {
				if (x.displayID == display) {
					d = x;
				}
			}
			if (!d) {
				seterr(err, errlen, @"the display is gone");
				return NULL;
			}

			SCContentFilter *filter = [[SCContentFilter alloc] initWithDisplay:d excludingWindows:@[]];
			SCStreamConfiguration *cfg = [[SCStreamConfiguration alloc] init];
			cfg.width = width;
			cfg.height = height;
			cfg.pixelFormat = kCVPixelFormatType_32BGRA;
			cfg.colorSpaceName = kCGColorSpaceSRGB;
			cfg.showsCursor = NO; // the cursor goes separately
			cfg.minimumFrameInterval = CMTimeMake(1, 60);
			// One buffer stays with HBOutput until the next frame.
			cfg.queueDepth = 5;

			HBOutput *out = [[HBOutput alloc] init];
			SCStream *st = [[SCStream alloc] initWithFilter:filter configuration:cfg delegate:out];
			dispatch_queue_t q = dispatch_queue_create("helpbuoy.capture", DISPATCH_QUEUE_SERIAL);
			NSError *aerr = nil;
			if (![st addStreamOutput:out type:SCStreamOutputTypeScreen sampleHandlerQueue:q error:&aerr]) {
				seterr(err, errlen, aerr.localizedDescription);
				return NULL;
			}
			__block NSError *serr = nil;
			[st startCaptureWithCompletionHandler:^(NSError *e) {
				serr = e;
				dispatch_semaphore_signal(sem);
			}];
			if (dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, waitSeconds * NSEC_PER_SEC))) {
				seterr(err, errlen, @"the screen capture did not start");
				return NULL;
			}
			if (serr) {
				seterr(err, errlen, serr.localizedDescription);
				return NULL;
			}
			hb_stream *s = calloc(1, sizeof *s);
			s->stream = (void *)CFBridgingRetain(st);
			s->output = (void *)CFBridgingRetain(out);
			return s;
		}
	}
	seterr(err, errlen, @"screen capture needs macOS 12.3 or later");
	return NULL;
}

int hb_stream_copy(hb_stream *s, uint8_t *dst, int stride, int w, int h, uint64_t *seq, char *err, int errlen) {
	if (@available(macOS 12.3, *)) {
		HBOutput *out = (__bridge HBOutput *)s->output;
		return [out copyTo:dst stride:stride width:w height:h seq:seq err:err len:errlen];
	}
	return -1;
}

void hb_stream_stop(hb_stream *s) {
	if (@available(macOS 12.3, *)) {
		SCStream *st = CFBridgingRelease(s->stream);
		id out = CFBridgingRelease(s->output);
		dispatch_semaphore_t sem = dispatch_semaphore_create(0);
		[st stopCaptureWithCompletionHandler:^(NSError *e) {
			dispatch_semaphore_signal(sem);
		}];
		dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 2 * NSEC_PER_SEC));
		(void)out;
	}
	free(s);
}

int hb_cursor_pos(double *x, double *y) {
	CGEventRef e = CGEventCreate(NULL);
	if (!e) {
		return -1;
	}
	CGPoint p = CGEventGetLocation(e);
	CFRelease(e);
	*x = p.x;
	*y = p.y;
	return 0;
}

int hb_cursor_shape(double scale, uint8_t *rgba, int max, int *w, int *h, int *hotx, int *hoty) {
	@autoreleasepool {
		NSCursor *c = [NSCursor currentSystemCursor];
		if (!c || !c.image) {
			return -1;
		}
		NSSize sz = c.image.size;
		int pw = (int)(sz.width * scale + 0.5), ph = (int)(sz.height * scale + 0.5);
		if (pw <= 0 || ph <= 0 || pw * ph * 4 > max) {
			return -1;
		}
		CGColorSpaceRef cs = CGColorSpaceCreateWithName(kCGColorSpaceSRGB);
		CGContextRef ctx = CGBitmapContextCreate(rgba, pw, ph, 8, pw * 4, cs, kCGImageAlphaPremultipliedLast | kCGBitmapByteOrder32Big);
		CGColorSpaceRelease(cs);
		if (!ctx) {
			return -1;
		}
		CGContextClearRect(ctx, CGRectMake(0, 0, pw, ph));
		NSGraphicsContext *g = [NSGraphicsContext graphicsContextWithCGContext:ctx flipped:NO];
		[NSGraphicsContext saveGraphicsState];
		NSGraphicsContext.currentContext = g;
		[c.image drawInRect:NSMakeRect(0, 0, pw, ph) fromRect:NSZeroRect operation:NSCompositingOperationCopy fraction:1];
		[NSGraphicsContext restoreGraphicsState];
		CGContextFlush(ctx);
		CGContextRelease(ctx);
		// The hot spot is in points from the top left of the image.
		*w = pw;
		*h = ph;
		*hotx = (int)(c.hotSpot.x * scale + 0.5);
		*hoty = (int)(c.hotSpot.y * scale + 0.5);
		return 0;
	}
}
