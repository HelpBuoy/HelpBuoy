#import <AppKit/AppKit.h>
#include <ApplicationServices/ApplicationServices.h>

#include "cgevent.h"

static CGEventSourceRef src;

int hb_inject_open(void) {
	if (src) {
		return 0;
	}
	src = CGEventSourceCreate(kCGEventSourceStateHIDSystemState);
	if (!src) {
		return -1;
	}
	CGEventSourceSetUserData(src, HB_MAGIC);
	// Don't hold back the local mouse and keyboard after each injected
	// event: the person in front of the Mac must be able to take over.
	CGEventSourceSetLocalEventsSuppressionInterval(src, 0);
	return 0;
}

void hb_inject_close(void) {
	if (src) {
		CFRelease(src);
		src = NULL;
	}
}

static int post(CGEventRef e, uint64_t flags) {
	if (!e) {
		return -1;
	}
	CGEventSetIntegerValueField(e, kCGEventSourceUserData, HB_MAGIC);
	CGEventSetFlags(e, (CGEventFlags)flags);
	CGEventPost(kCGHIDEventTap, e);
	CFRelease(e);
	return 0;
}

int hb_inject_key(uint16_t code, int down, uint64_t flags, int modifier) {
	CGEventRef e = CGEventCreateKeyboardEvent(src, (CGKeyCode)code, down != 0);
	if (e && modifier) {
		CGEventSetType(e, kCGEventFlagsChanged);
	}
	return post(e, flags);
}

int hb_inject_mouse(uint32_t type, uint32_t button, double x, double y, int64_t clicks, uint64_t flags) {
	CGEventRef e = CGEventCreateMouseEvent(src, (CGEventType)type, CGPointMake(x, y), (CGMouseButton)button);
	if (e && clicks > 0) {
		CGEventSetIntegerValueField(e, kCGMouseEventClickState, clicks);
	}
	return post(e, flags);
}

int hb_inject_scroll(int32_t dy, int32_t dx, uint64_t flags) {
	return post(CGEventCreateScrollWheelEvent2(src, kCGScrollEventUnitLine, 2, dy, dx, 0), flags);
}

int hb_inject_text(const uint16_t *utf16, int n) {
	for (int down = 1; down >= 0; down--) {
		CGEventRef e = CGEventCreateKeyboardEvent(src, 0, down);
		if (!e) {
			return -1;
		}
		CGEventKeyboardSetUnicodeString(e, (UniCharCount)n, utf16);
		// No modifiers: a held Command must not turn the text into shortcuts.
		if (post(e, 0) != 0) {
			return -1;
		}
	}
	return 0;
}

double hb_double_click(void) {
	return [NSEvent doubleClickInterval];
}

void hb_main_display(double *x, double *y, double *w, double *h) {
	CGRect r = CGDisplayBounds(CGMainDisplayID());
	*x = r.origin.x;
	*y = r.origin.y;
	*w = r.size.width;
	*h = r.size.height;
}

// The Esc tap. It runs on its own thread with its own run loop.

extern void hbEscPressed(void);

static CFMachPortRef tap;
static CFRunLoopSourceRef tapSource;
static CFRunLoopRef tapLoop;

static CGEventRef escCallback(CGEventTapProxy proxy, CGEventType type, CGEventRef e, void *ref) {
	if (type == kCGEventTapDisabledByTimeout || type == kCGEventTapDisabledByUserInput) {
		if (tap) {
			CGEventTapEnable(tap, true);
		}
		return e;
	}
	if ((type == kCGEventKeyDown || type == kCGEventKeyUp) &&
	    CGEventGetIntegerValueField(e, kCGKeyboardEventKeycode) == 53 &&
	    CGEventGetIntegerValueField(e, kCGEventSourceUserData) != HB_MAGIC) {
		if (type == kCGEventKeyDown && !CGEventGetIntegerValueField(e, kCGKeyboardEventAutorepeat)) {
			hbEscPressed();
		}
		return NULL; // swallow the local Esc, down and up
	}
	return e;
}

int hb_esc_create(void) {
	CGEventMask mask = CGEventMaskBit(kCGEventKeyDown) | CGEventMaskBit(kCGEventKeyUp);
	tap = CGEventTapCreate(kCGSessionEventTap, kCGHeadInsertEventTap, kCGEventTapOptionDefault, mask, escCallback, NULL);
	if (!tap) {
		return -1;
	}
	tapSource = CFMachPortCreateRunLoopSource(NULL, tap, 0);
	tapLoop = CFRunLoopGetCurrent();
	CFRetain(tapLoop);
	CFRunLoopAddSource(tapLoop, tapSource, kCFRunLoopCommonModes);
	CGEventTapEnable(tap, true);
	return 0;
}

void hb_esc_run(void) {
	CFRunLoopRun();
	CGEventTapEnable(tap, false);
	CFRunLoopRemoveSource(tapLoop, tapSource, kCFRunLoopCommonModes);
	CFRelease(tapSource);
	CFMachPortInvalidate(tap);
	CFRelease(tap);
	CFRelease(tapLoop);
	tap = NULL;
	tapSource = NULL;
	tapLoop = NULL;
}

void hb_esc_stop(void) {
	if (tapLoop) {
		CFRunLoopStop(tapLoop);
	}
}
