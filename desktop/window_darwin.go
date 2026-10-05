//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa -framework WebKit

#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#import <objc/runtime.h>
#include <stdlib.h>

// A compact unified title bar: the traffic lights sit centred in a 38pt strip, which is
// the height of the app's top bar.
static void skyCompactTitleBar(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		if (@available(macOS 11.0, *)) {
			for (NSWindow *w in [NSApp windows]) {
				if (w.toolbar != nil) {
					w.toolbarStyle = NSWindowToolbarStyleUnifiedCompact;
				}
			}
		}
	});
}

// A hidden dev instance stays out of the Dock and the app switcher.
static void skyBackground(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		[NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
	});
}

extern void skyFrameChanged(void);

static NSWindow *skyWindow(void) {
	for (NSWindow *w in [NSApp windows]) {
		if (w.toolbar != nil || w.contentView.subviews.count > 0) {
			return w;
		}
	}
	return nil;
}

static NSView *skyFindWebView(NSView *v) {
	if ([v isKindOfClass:NSClassFromString(@"WKWebView")]) {
		return v;
	}
	for (NSView *c in v.subviews) {
		NSView *f = skyFindWebView(c);
		if (f != nil) {
			return f;
		}
	}
	return nil;
}

// A frame the screens can show: on the screen that shows most of it (the main one when the
// display it was on is gone), no bigger than that screen, and wholly inside it.
static NSRect skyFit(NSRect r) {
	NSScreen *best = nil;
	CGFloat most = 0;
	for (NSScreen *s in [NSScreen screens]) {
		NSRect i = NSIntersectionRect(r, s.visibleFrame);
		if (i.size.width * i.size.height > most) {
			most = i.size.width * i.size.height;
			best = s;
		}
	}
	if (best == nil) {
		best = [NSScreen mainScreen] ?: [NSScreen screens].firstObject;
	}
	if (best == nil) {
		return r;
	}
	NSRect v = best.visibleFrame;
	r.size.width = MIN(r.size.width, v.size.width);
	r.size.height = MIN(r.size.height, v.size.height);
	r.origin.x = MAX(v.origin.x, MIN(r.origin.x, NSMaxX(v) - r.size.width));
	r.origin.y = MAX(v.origin.y, MIN(r.origin.y, NSMaxY(v) - r.size.height));
	return r;
}

// Places the window before it first shows: where it was (has), or filling the screen the
// user is on. Then watches it, so every move and resize is saved. Wails centres the window
// in a block it queued before launch, which runs after this, so the frame is put back once
// more behind that block (not when starting full screen: that transition is under way).
static void skyLaunchFrame(int has, int full, double x, double y, double w, double h) {
	[[NSNotificationCenter defaultCenter] addObserverForName:NSApplicationWillFinishLaunchingNotification object:nil queue:nil usingBlock:^(NSNotification *note) {
		NSWindow *win = skyWindow();
		NSScreen *screen = [NSScreen mainScreen] ?: [NSScreen screens].firstObject;
		if (win == nil || (!has && screen == nil)) {
			return;
		}
		NSRect r = has ? skyFit(NSMakeRect(x, y, w, h)) : screen.visibleFrame;
		[win setFrame:r display:NO];
		dispatch_async(dispatch_get_main_queue(), ^{
			if (!full) {
				[win setFrame:r display:YES];
			}
			NSNotificationCenter *c = [NSNotificationCenter defaultCenter];
			for (NSNotificationName name in @[NSWindowDidMoveNotification, NSWindowDidResizeNotification, NSWindowDidEndLiveResizeNotification, NSWindowDidEnterFullScreenNotification, NSWindowDidExitFullScreenNotification]) {
				[c addObserverForName:name object:win queue:nil usingBlock:^(NSNotification *n) {
					skyFrameChanged();
				}];
			}
		});
	}];
}

static int skyGetFrame(double *x, double *y, double *w, double *h, int *full, int *zoomed) {
	__block int ok = 0;
	void (^read)(void) = ^{
		NSWindow *win = skyWindow();
		if (win == nil) {
			return;
		}
		NSRect r = win.frame;
		*x = r.origin.x;
		*y = r.origin.y;
		*w = r.size.width;
		*h = r.size.height;
		*full = (win.styleMask & NSWindowStyleMaskFullScreen) != 0;
		*zoomed = win.isZoomed;
		ok = 1;
	};
	if ([NSThread isMainThread]) {
		read();
	} else {
		dispatch_sync(dispatch_get_main_queue(), read);
	}
	return ok;
}

// A hidden dev instance must never take the keyboard from the app the user is working in.
// Wails activates the app when it finishes launching, hidden window or not; this makes
// that a no-op for the life of the process.
static void skyNeverActivate(void) {
	Method m = class_getInstanceMethod([NSApplication class], @selector(activateIgnoringOtherApps:));
	if (m != NULL) {
		method_setImplementation(m, imp_implementationWithBlock(^(id app, BOOL flag) {}));
	}
}

// Dev checks only: a key press delivered the way AppKit delivers one to the key window of
// the active app. The window's views see a key equivalent first (the web view hands it to
// the page, and resends it if the page leaves it alone), then the main menu, then keyDown.
static void skyDebugKey(int flagsOnly, const char *chars, const char *plain, int code, unsigned long flags) {
	NSString *c = [NSString stringWithUTF8String:chars];
	NSString *p = [NSString stringWithUTF8String:plain];
	dispatch_async(dispatch_get_main_queue(), ^{
		NSWindow *w = skyWindow();
		if (w == nil) {
			return;
		}
		NSView *web = skyFindWebView(w.contentView);
		if (web != nil && w.firstResponder != web) {
			[w makeFirstResponder:web];
		}
		NSTimeInterval now = [[NSProcessInfo processInfo] systemUptime];
		if (flagsOnly) {
			NSEvent *e = [NSEvent keyEventWithType:NSEventTypeFlagsChanged location:NSZeroPoint modifierFlags:flags timestamp:now windowNumber:w.windowNumber context:nil characters:@"" charactersIgnoringModifiers:@"" isARepeat:NO keyCode:code];
			[w sendEvent:e];
			return;
		}
		NSEvent *down = [NSEvent keyEventWithType:NSEventTypeKeyDown location:NSZeroPoint modifierFlags:flags timestamp:now windowNumber:w.windowNumber context:nil characters:c charactersIgnoringModifiers:p isARepeat:NO keyCode:code];
		BOOL equivalent = (flags & (NSEventModifierFlagCommand | NSEventModifierFlagControl)) != 0;
		if (!(equivalent && ([w performKeyEquivalent:down] || [[NSApp mainMenu] performKeyEquivalent:down]))) {
			[w sendEvent:down];
		}
		NSEvent *up = [NSEvent keyEventWithType:NSEventTypeKeyUp location:NSZeroPoint modifierFlags:flags timestamp:now + 0.03 windowNumber:w.windowNumber context:nil characters:c charactersIgnoringModifiers:p isARepeat:NO keyCode:code];
		[w sendEvent:up];
	});
}

// Dev checks only: a mouse click at (x, y) in the web view's coordinates (top left origin),
// with modifier flags, sent to the window as AppKit events, the way a real click arrives.
static void skyDebugClick(double x, double y, unsigned long flags) {
	dispatch_async(dispatch_get_main_queue(), ^{
		NSWindow *w = skyWindow();
		if (w == nil) {
			return;
		}
		NSView *web = skyFindWebView(w.contentView);
		if (web == nil) {
			return;
		}
		NSPoint p = [web convertPoint:NSMakePoint(x, web.isFlipped ? y : web.bounds.size.height - y) toView:nil];
		NSTimeInterval now = [[NSProcessInfo processInfo] systemUptime];
		NSEvent *down = [NSEvent mouseEventWithType:NSEventTypeLeftMouseDown location:p modifierFlags:flags timestamp:now windowNumber:w.windowNumber context:nil eventNumber:0 clickCount:1 pressure:1];
		NSEvent *up = [NSEvent mouseEventWithType:NSEventTypeLeftMouseUp location:p modifierFlags:flags timestamp:now + 0.05 windowNumber:w.windowNumber context:nil eventNumber:0 clickCount:1 pressure:0];
		[w sendEvent:down];
		[w sendEvent:up];
	});
}

// Dev checks only: orders the hidden window in so the web view lays out and animates as it
// would on screen, but fully transparent, deaf to the mouse, behind everything and left out
// of Mission Control, so nothing shows. (Moving it off every screen doesn't work: the
// system puts it back on one.) The web view is told not to treat that as being covered.
static int skyDebugStage(void) {
	__block int number = 0;
	void (^stage)(void) = ^{
		NSWindow *w = skyWindow();
		if (w == nil) {
			return;
		}
		NSView *web = skyFindWebView(w.contentView);
		SEL sel = NSSelectorFromString(@"_setWindowOcclusionDetectionEnabled:");
		if (web != nil && [web respondsToSelector:sel]) {
			NSInvocation *inv = [NSInvocation invocationWithMethodSignature:[web methodSignatureForSelector:sel]];
			BOOL off = NO;
			inv.selector = sel;
			inv.target = web;
			[inv setArgument:&off atIndex:2];
			[inv invoke];
		}
		w.alphaValue = 0;
		w.ignoresMouseEvents = YES;
		w.hasShadow = NO;
		w.collectionBehavior = NSWindowCollectionBehaviorTransient | NSWindowCollectionBehaviorIgnoresCycle | NSWindowCollectionBehaviorStationary;
		[w orderBack:nil];
		if (web != nil) {
			[w makeFirstResponder:web];
		}
		number = (int)w.windowNumber;
	};
	if ([NSThread isMainThread]) {
		stage();
	} else {
		dispatch_sync(dispatch_get_main_queue(), stage);
	}
	return number;
}

// Dev checks only: moves and sizes the window, as the user dragging it would.
static void skyDebugSetFrame(double x, double y, double w, double h) {
	dispatch_async(dispatch_get_main_queue(), ^{
		[skyWindow() setFrame:NSMakeRect(x, y, w, h) display:YES];
	});
}

// Dev checks only: the usable area of each screen, "x,y,w,h;…".
static const char *skyDebugScreens(void) {
	__block NSMutableString *out = [NSMutableString string];
	void (^read)(void) = ^{
		for (NSScreen *s in [NSScreen screens]) {
			NSRect v = s.visibleFrame;
			[out appendFormat:@"%.0f,%.0f,%.0f,%.0f@%.0fx;", v.origin.x, v.origin.y, v.size.width, v.size.height, s.backingScaleFactor];
		}
	};
	if ([NSThread isMainThread]) {
		read();
	} else {
		dispatch_sync(dispatch_get_main_queue(), read);
	}
	return strdup(out.UTF8String);
}

// Dev checks only: a picture of what the web view shows, written as a PNG.
static void skyDebugSnapshot(const char *path) {
	NSString *file = [NSString stringWithUTF8String:path];
	dispatch_async(dispatch_get_main_queue(), ^{
		NSWindow *w = skyWindow();
		WKWebView *web = w == nil ? nil : (WKWebView *)skyFindWebView(w.contentView);
		if (web == nil) {
			return;
		}
		[web takeSnapshotWithConfiguration:nil completionHandler:^(NSImage *image, NSError *error) {
			if (image == nil) {
				return;
			}
			NSBitmapImageRep *rep = [[NSBitmapImageRep alloc] initWithData:[image TIFFRepresentation]];
			[[rep representationUsingType:NSBitmapImageFileTypePNG properties:@{}] writeToFile:file atomically:YES];
		}];
	});
}

// Dev checks only: makes the page render as it would on a display with this scale factor
// (the hidden window sits on no display, so it would otherwise render at 1x).
static void skyDebugScale(double scale) {
	dispatch_async(dispatch_get_main_queue(), ^{
		NSWindow *w = skyWindow();
		NSView *web = w == nil ? nil : skyFindWebView(w.contentView);
		SEL sel = NSSelectorFromString(@"_setOverrideDeviceScaleFactor:");
		if (web != nil && [web respondsToSelector:sel]) {
			NSInvocation *inv = [NSInvocation invocationWithMethodSignature:[web methodSignatureForSelector:sel]];
			CGFloat f = scale;
			inv.selector = sel;
			inv.target = web;
			[inv setArgument:&f atIndex:2];
			[inv invoke];
		}
	});
}

// Since macOS 14 an app may only come forward if the active app yields to it, so this
// window yields first.
static int skyActivate(int pid) {
	NSRunningApplication *app = [NSRunningApplication runningApplicationWithProcessIdentifier:pid];
	if (app == nil) {
		return 0;
	}
	dispatch_async(dispatch_get_main_queue(), ^{
		if (@available(macOS 14.0, *)) {
			[NSApp yieldActivationToApplication:app];
		}
		[app activateWithOptions:NSApplicationActivateAllWindows];
	});
	return 1;
}

// The Dock icon (no bytes: the bundle's own). With a bundle path it also becomes the app's
// icon in Finder (no bytes: the custom one is removed), which the Dock shows while the app
// is closed.
static void skySetIcon(const void *bytes, int n, const char *bundle) {
	@autoreleasepool {
		NSData *data = bytes != NULL ? [[NSData alloc] initWithBytes:bytes length:n] : nil;
		NSString *path = bundle != NULL ? [[NSString alloc] initWithUTF8String:bundle] : nil;
		dispatch_async(dispatch_get_main_queue(), ^{
			NSImage *img = data != nil ? [[NSImage alloc] initWithData:data] : nil;
			[NSApp setApplicationIconImage:img];
			if (path != nil) {
				[[NSWorkspace sharedWorkspace] setIcon:img forFile:path options:0];
			}
			[img release];
			[data release];
			[path release];
		});
	}
}
*/
import "C"

import (
	"context"
	"errors"
	"unsafe"
)

// compactTitleBar shrinks the macOS title bar to the height of the top bar.
func compactTitleBar() { C.skyCompactTitleBar() }

// backgroundApp keeps a headless dev instance out of the Dock.
func backgroundApp() { C.skyBackground() }

func debugClick(x, y float64, mods []string) {
	var flags C.ulong
	for _, m := range mods {
		switch m {
		case "cmd":
			flags |= C.NSEventModifierFlagCommand
		case "shift":
			flags |= C.NSEventModifierFlagShift
		case "alt":
			flags |= C.NSEventModifierFlagOption
		case "ctrl":
			flags |= C.NSEventModifierFlagControl
		}
	}
	C.skyDebugClick(C.double(x), C.double(y), flags)
}

func debugKey(kind, chars, plain string, code int, mods []string) {
	var flags C.ulong
	for _, m := range mods {
		switch m {
		case "cmd":
			flags |= C.NSEventModifierFlagCommand
		case "shift":
			flags |= C.NSEventModifierFlagShift
		case "alt":
			flags |= C.NSEventModifierFlagOption
		case "ctrl":
			flags |= C.NSEventModifierFlagControl
		}
	}
	c, p := C.CString(chars), C.CString(plain)
	defer C.free(unsafe.Pointer(c))
	defer C.free(unsafe.Pointer(p))
	only := C.int(0)
	if kind == "flags" {
		only = 1
	}
	C.skyDebugKey(only, c, p, C.int(code), flags)
}

func debugStage() int { return int(C.skyDebugStage()) }

func debugSnapshot(path string) {
	p := C.CString(path)
	defer C.free(unsafe.Pointer(p))
	C.skyDebugSnapshot(p)
}

func debugSetFrame(f frame) {
	C.skyDebugSetFrame(C.double(f.X), C.double(f.Y), C.double(f.W), C.double(f.H))
}

func debugScreens() string {
	c := C.skyDebugScreens()
	defer C.free(unsafe.Pointer(c))
	return C.GoString(c)
}

// neverActivate keeps a hidden dev instance from taking the keyboard when it launches.
func neverActivate() { C.skyNeverActivate() }

// frameDown is the direction of "down" in window coordinates (AppKit's y axis points up).
const frameDown = -1

// launchFrame places the window before it first shows and has its frame saved as it changes.
func launchFrame(f frame, ok bool) {
	has := C.int(0)
	if ok {
		has = 1
	}
	full := C.int(0)
	if f.Fullscreen {
		full = 1
	}
	C.skyLaunchFrame(has, full, C.double(f.X), C.double(f.Y), C.double(f.W), C.double(f.H))
}

func currentFrame(context.Context) (frame, bool) {
	var x, y, w, h C.double
	var full, zoomed C.int
	if C.skyGetFrame(&x, &y, &w, &h, &full, &zoomed) == 0 {
		return frame{}, false
	}
	return frame{X: int(x), Y: int(y), W: int(w), H: int(h), Fullscreen: full != 0, Maximised: zoomed != 0}, true
}

func debugScale(f float64) { C.skyDebugScale(C.double(f)) }

// activate brings another app (a Lungo window) to the front.
func activate(pid int) error {
	if C.skyActivate(C.int(pid)) == 0 {
		return errors.New("couldn't bring that window forward")
	}
	return nil
}

// setDockIcon shows png in the Dock (nil: the bundle's own icon). With bundle it also
// becomes the app's icon in Finder.
func setDockIcon(png []byte, bundle bool) {
	var p unsafe.Pointer
	if len(png) > 0 {
		p = C.CBytes(png)
		defer C.free(p)
	}
	var path *C.char
	if b, ok := appBundle(); ok && bundle {
		path = C.CString(b)
		defer C.free(unsafe.Pointer(path))
	}
	C.skySetIcon(p, C.int(len(png)), path)
}
