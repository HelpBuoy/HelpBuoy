#import <AppKit/AppKit.h>
#import <QuartzCore/QuartzCore.h>

#include "host.h"

extern void hbEvent(int kind, int a, int b, int c, int d, char *text);

static void post(int kind, int a, int b, int c, int d, NSString *text) {
	hbEvent(kind, a, b, c, d, (char *)(text ? text.UTF8String : ""));
}

static NSWindow *window;
static double scale = 1;
static NSInteger attention;
static BOOL quitting, terminatePending;

// HBView shows the window's pixels and turns input into events.
@interface HBView : NSView
@end

@implementation HBView

- (BOOL)isFlipped {
	return YES; // y grows down, as in the drawn window
}

- (BOOL)acceptsFirstResponder {
	return YES;
}

- (BOOL)acceptsFirstMouse:(NSEvent *)e {
	return YES;
}

- (void)updateTrackingAreas {
	for (NSTrackingArea *t in self.trackingAreas) {
		[self removeTrackingArea:t];
	}
	NSTrackingAreaOptions o = NSTrackingMouseMoved | NSTrackingMouseEnteredAndExited | NSTrackingActiveAlways | NSTrackingInVisibleRect;
	[self addTrackingArea:[[NSTrackingArea alloc] initWithRect:NSZeroRect options:o owner:self userInfo:nil]];
	[super updateTrackingAreas];
}

- (void)mouse:(NSEvent *)e kind:(int)kind drag:(BOOL)drag {
	NSPoint p = [self convertPoint:e.locationInWindow fromView:nil];
	post(hbEvMouse, kind, (int)(p.x * scale + 0.5), (int)(p.y * scale + 0.5), drag, nil);
}

- (void)mouseDown:(NSEvent *)e {
	[self mouse:e kind:0 drag:NO];
}

- (void)mouseUp:(NSEvent *)e {
	[self mouse:e kind:1 drag:NO];
}

- (void)mouseMoved:(NSEvent *)e {
	[self mouse:e kind:2 drag:NO];
}

- (void)mouseDragged:(NSEvent *)e {
	[self mouse:e kind:2 drag:YES];
}

- (void)mouseExited:(NSEvent *)e {
	post(hbEvMouse, 3, 0, 0, 0, nil);
}

// key turns a key press into a hbEvKey event.
- (void)key:(NSEvent *)e {
	NSEventModifierFlags m = e.modifierFlags;
	BOOL cmd = (m & NSEventModifierFlagCommand) != 0;
	BOOL ctrl = (m & NSEventModifierFlagControl) != 0;
	int flags = 0;
	if (m & NSEventModifierFlagOption) {
		flags |= hbKeyWord;
	}
	if (m & NSEventModifierFlagShift) {
		flags |= hbKeyShift;
	}
	if (e.isARepeat) {
		flags |= hbKeyRepeat;
	}
	int key = hbKeyNone;
	switch (e.keyCode) {
	case 36: // Return
	case 76: // keypad Enter
		key = hbKeyEnter;
		break;
	case 53:
		key = hbKeyEscape;
		break;
	case 48:
		key = hbKeyTab;
		break;
	case 51:
		key = hbKeyBackspace;
		break;
	case 117:
		key = hbKeyDelete;
		break;
	case 123: // Command+Left goes to the start, as in every Mac text field
		key = cmd ? hbKeyHome : hbKeyLeft;
		cmd = NO;
		break;
	case 124:
		key = cmd ? hbKeyEnd : hbKeyRight;
		cmd = NO;
		break;
	case 115:
		key = hbKeyHome;
		break;
	case 119:
		key = hbKeyEnd;
		break;
	case 114:
		key = hbKeyInsert;
		break;
	}
	if (cmd) {
		flags |= hbKeyCmd;
	}
	int ch = 0;
	NSString *plain = e.charactersIgnoringModifiers.lowercaseString;
	if (plain.length > 0) {
		unichar u = [plain characterAtIndex:0];
		if (u >= 0x20 && u != 0x7f && (u < 0xF700 || u > 0xF8FF)) {
			ch = u;
		}
	}
	NSString *text = nil;
	if (key == hbKeyNone && !cmd && !ctrl) {
		text = e.characters;
		for (NSUInteger i = 0; i < text.length; i++) {
			unichar u = [text characterAtIndex:i];
			if (u < 0x20 || u == 0x7f || (u >= 0xF700 && u <= 0xF8FF)) {
				text = nil; // a function key, not text
				break;
			}
		}
	}
	post(hbEvKey, key, flags, ch, 0, text);
}

- (void)keyDown:(NSEvent *)e {
	[self key:e];
}

// performKeyEquivalent gets Command shortcuts before the menu: the window
// handles copy, cut, paste and select all itself.
- (BOOL)performKeyEquivalent:(NSEvent *)e {
	NSEventModifierFlags m = e.modifierFlags & NSEventModifierFlagDeviceIndependentFlagsMask;
	if (e.type == NSEventTypeKeyDown && window.isKeyWindow && (m & NSEventModifierFlagCommand) &&
	    !(m & (NSEventModifierFlagControl | NSEventModifierFlagOption))) {
		NSString *c = e.charactersIgnoringModifiers.lowercaseString;
		if ([c isEqualToString:@"a"] || [c isEqualToString:@"c"] || [c isEqualToString:@"v"] || [c isEqualToString:@"x"] ||
		    e.keyCode == 123 || e.keyCode == 124) {
			[self key:e];
			return YES;
		}
	}
	return [super performKeyEquivalent:e];
}
@end

// HBDelegate is the window's and the application's delegate.
@interface HBDelegate : NSObject <NSWindowDelegate, NSApplicationDelegate>
@end

@implementation HBDelegate

- (BOOL)windowShouldClose:(NSWindow *)w {
	post(hbEvClose, 0, 0, 0, 0, nil);
	return NO; // the window's goroutine decides
}

- (void)windowDidBecomeKey:(NSNotification *)n {
	post(hbEvFocus, 1, 0, 0, 0, nil);
}

- (void)windowDidResignKey:(NSNotification *)n {
	post(hbEvFocus, 0, 0, 0, 0, nil);
}

// applicationShouldTerminate lets the window's goroutine finish first; it
// calls hb_app_quit when done. Quitting this way, not cancelling, keeps
// "Quit & Reopen" in System Settings working.
- (NSApplicationTerminateReply)applicationShouldTerminate:(NSApplication *)app {
	if (quitting) {
		return NSTerminateNow;
	}
	terminatePending = YES;
	post(hbEvClose, 0, 0, 0, 0, nil);
	return NSTerminateLater;
}

- (BOOL)applicationShouldHandleReopen:(NSApplication *)app hasVisibleWindows:(BOOL)visible {
	if (window) {
		[window deminiaturize:nil];
		[window makeKeyAndOrderFront:nil];
	}
	return NO;
}
@end

static HBDelegate *delegate;
static HBView *view;

static NSMenuItem *item(NSString *title, SEL action, NSString *key) {
	return [[NSMenuItem alloc] initWithTitle:title action:action keyEquivalent:key];
}

void hb_app_init(const char *hide, const char *quit, const char *minimize, const uint8_t *icon, int iconLen) {
	[NSApplication sharedApplication];
	NSApp.activationPolicy = NSApplicationActivationPolicyRegular;
	delegate = [[HBDelegate alloc] init];
	NSApp.delegate = delegate;

	NSMenu *bar = [[NSMenu alloc] init];
	NSMenuItem *appItem = [[NSMenuItem alloc] init];
	NSMenu *appMenu = [[NSMenu alloc] init];
	[appMenu addItem:item(@(hide), @selector(hide:), @"h")];
	[appMenu addItem:[NSMenuItem separatorItem]];
	[appMenu addItem:item(@(quit), @selector(terminate:), @"q")];
	appItem.submenu = appMenu;
	[bar addItem:appItem];
	NSMenuItem *winItem = [[NSMenuItem alloc] init];
	NSMenu *winMenu = [[NSMenu alloc] initWithTitle:@"Window"];
	[winMenu addItem:item(@(minimize), @selector(performMiniaturize:), @"m")];
	winItem.submenu = winMenu;
	[bar addItem:winItem];
	NSApp.mainMenu = bar;

	// Started as a bare binary there is no bundle icon.
	if (iconLen > 0 && ![[NSBundle mainBundle] objectForInfoDictionaryKey:@"CFBundleIconFile"]) {
		NSImage *img = [[NSImage alloc] initWithData:[NSData dataWithBytes:icon length:iconLen]];
		if (img) {
			NSApp.applicationIconImage = img;
		}
	}
}

void hb_app_run(void) {
	[NSApp run];
}

void hb_app_quit(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		quitting = YES;
		if (terminatePending) {
			[NSApp replyToApplicationShouldTerminate:YES];
		} else {
			[NSApp terminate:nil];
		}
	});
}

int hb_show_running(int pid) {
	NSRunningApplication *app = [NSRunningApplication runningApplicationWithProcessIdentifier:pid];
	if (!app) {
		return -1;
	}
	[app unhide];
	return [app activateWithOptions:NSApplicationActivateIgnoringOtherApps] ? 0 : -1;
}

int hb_win_open(const char *title, int w, int h, double *outScale) {
	NSString *t = @(title);
	__block int ok = 0;
	dispatch_sync(dispatch_get_main_queue(), ^{
		NSScreen *s = NSScreen.mainScreen;
		scale = s ? s.backingScaleFactor : 1;
		if (scale < 1) {
			scale = 1;
		}
		NSWindowStyleMask style = NSWindowStyleMaskTitled | NSWindowStyleMaskClosable | NSWindowStyleMaskMiniaturizable;
		window = [[NSWindow alloc] initWithContentRect:NSMakeRect(0, 0, w, h) styleMask:style backing:NSBackingStoreBuffered defer:NO];
		window.releasedWhenClosed = NO;
		window.title = t;
		window.delegate = delegate;
		window.acceptsMouseMovedEvents = YES;
		view = [[HBView alloc] initWithFrame:NSMakeRect(0, 0, w, h)];
		view.wantsLayer = YES;
		view.layer.contentsGravity = kCAGravityResize;
		view.layer.backgroundColor = CGColorCreateGenericRGB(0xf0 / 255.0, 0xf0 / 255.0, 0xf0 / 255.0, 1);
		window.contentView = view;
		[window makeFirstResponder:view];
		[window center];
		ok = 1;
	});
	*outScale = scale;
	return ok ? 0 : -1;
}

void hb_win_show(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		[window makeKeyAndOrderFront:nil];
		[NSApp activateIgnoringOtherApps:YES];
		post(hbEvExpose, 0, 0, 0, 0, nil);
	});
}

void hb_win_put(const uint8_t *rgba, int w, int h, int stride) {
	CFDataRef data = CFDataCreate(NULL, rgba, (CFIndex)stride * h);
	CGDataProviderRef dp = CGDataProviderCreateWithCFData(data);
	CFRelease(data);
	CGColorSpaceRef cs = CGColorSpaceCreateWithName(kCGColorSpaceSRGB);
	CGImageRef img = CGImageCreate(w, h, 8, 32, stride, cs, kCGImageAlphaNoneSkipLast | kCGBitmapByteOrder32Big, dp, NULL, false,
	                               kCGRenderingIntentDefault);
	CGColorSpaceRelease(cs);
	CGDataProviderRelease(dp);
	if (!img) {
		return;
	}
	dispatch_async(dispatch_get_main_queue(), ^{
		view.layer.contents = (__bridge id)img;
		CGImageRelease(img);
	});
}

void hb_win_title(const char *title) {
	NSString *t = @(title);
	dispatch_async(dispatch_get_main_queue(), ^{
		window.title = t;
	});
}

void hb_win_minimize(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		[window miniaturize:nil];
	});
}

void hb_win_activate(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		if (window.miniaturized) {
			[window deminiaturize:nil];
		}
		[NSApp activateIgnoringOtherApps:YES];
		[window makeKeyAndOrderFront:nil];
	});
}

void hb_win_attention(int on) {
	dispatch_async(dispatch_get_main_queue(), ^{
		if (on && !attention) {
			attention = [NSApp requestUserAttention:NSCriticalRequest];
		} else if (!on && attention) {
			[NSApp cancelUserAttentionRequest:attention];
			attention = 0;
		}
	});
}

void hb_win_cursor(int kind) {
	dispatch_async(dispatch_get_main_queue(), ^{
		switch (kind) {
		case 1:
			[[NSCursor IBeamCursor] set];
			break;
		case 2:
			[[NSCursor pointingHandCursor] set];
			break;
		default:
			[[NSCursor arrowCursor] set];
		}
	});
}

void hb_win_close(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		[window orderOut:nil];
	});
}

void hb_clip_set(const char *text) {
	NSString *t = @(text);
	dispatch_async(dispatch_get_main_queue(), ^{
		NSPasteboard *pb = NSPasteboard.generalPasteboard;
		[pb clearContents];
		[pb setString:t forType:NSPasteboardTypeString];
	});
}

void hb_clip_request(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		NSString *s = [NSPasteboard.generalPasteboard stringForType:NSPasteboardTypeString];
		if (s) {
			post(hbEvPaste, 0, 0, 0, 0, s);
		}
	});
}
