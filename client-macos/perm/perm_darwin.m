#import <AppKit/AppKit.h>
#include <ApplicationServices/ApplicationServices.h>

#include "perm.h"

int hb_screen_allowed(void) {
	return CGPreflightScreenCaptureAccess() ? 1 : 0;
}

void hb_screen_request(void) {
	CGRequestScreenCaptureAccess();
}

int hb_input_allowed(int prompt) {
	NSDictionary *o = @{(__bridge NSString *)kAXTrustedCheckOptionPrompt : @(prompt != 0)};
	return AXIsProcessTrustedWithOptions((__bridge CFDictionaryRef)o) ? 1 : 0;
}

void hb_open_url(const char *url) {
	NSURL *u = [NSURL URLWithString:@(url)];
	dispatch_async(dispatch_get_main_queue(), ^{
		[[NSWorkspace sharedWorkspace] openURL:u];
	});
}
