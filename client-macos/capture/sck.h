// The C side of the capture package: displays from CoreGraphics, frames
// from ScreenCaptureKit and the cursor from AppKit. Coordinates are global
// display points, origin at the top left of the main display, the same
// space CGEvent uses.

#include <stdint.h>

typedef struct {
	uint32_t id;
	double x, y, w, h;
	int primary;
} hb_display;

// hb_displays fills out with up to max active displays, leaving out mirror
// copies, and returns the count, or -1.
int hb_displays(hb_display *out, int max);

typedef struct hb_stream hb_stream;

// hb_stream_start starts capturing display at width x height pixels,
// BGRA, without the cursor. It waits for the stream to run. On failure it
// returns NULL with a message in err.
hb_stream *hb_stream_start(uint32_t display, int width, int height, char *err, int errlen);

// hb_stream_copy copies the newest frame into dst (w x h BGRA pixels,
// stride bytes per row) if it is newer than *seq, and updates *seq. It
// returns 1 when it copied, 0 when there is nothing new and -1 when the
// stream stopped, with the reason in err.
int hb_stream_copy(hb_stream *s, uint8_t *dst, int stride, int w, int h, uint64_t *seq, char *err, int errlen);

// hb_stream_stop stops the stream and frees s.
void hb_stream_stop(hb_stream *s);

// hb_cursor_pos returns the pointer position in global points.
int hb_cursor_pos(double *x, double *y);

// hb_cursor_shape draws the current system cursor at scale pixels per
// point into rgba (premultiplied RGBA, top row first, at most max bytes).
// It returns 0 with the size and hot spot in pixels, or -1.
int hb_cursor_shape(double scale, uint8_t *rgba, int max, int *w, int *h, int *hotx, int *hoty);

// hb_has_metal reports whether the Mac has a Metal device. Without one
// (a virtual machine), WindowServer composites in software and crashes in
// WSSelectiveSharingUpdateDisplayStreamSurface as soon as a display stream
// runs, which ScreenCaptureKit and, on macOS 14, CGDisplayCreateImage use.
int hb_has_metal(void);

// hb_cg_copy grabs display with CGWindowListCreateImage (every on-screen
// window over the display's bounds, at points) and draws it into dst (w x h
// BGRA pixels, stride bytes per row), without the cursor. It returns 1, or
// -1 with a message in err. It makes a new image on every call and uses no
// display stream.
int hb_cg_copy(uint32_t display, uint8_t *dst, int stride, int w, int h, char *err, int errlen);
