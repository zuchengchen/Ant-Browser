//go:build darwin && cgo

package backend

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit -framework CoreGraphics
#import <AppKit/AppKit.h>
#import <CoreGraphics/CoreGraphics.h>
static int ant_focus_mac_browser(pid_t pid) {
    @autoreleasepool {
        NSRunningApplication *app = [NSRunningApplication runningApplicationWithProcessIdentifier:pid];
        if (!app) return 0;
        if (!app.active) [app activateWithOptions:NSApplicationActivateIgnoringOtherApps];
        if (!app.active) return 0;
        CFArrayRef windows = CGWindowListCopyWindowInfo(kCGWindowListOptionOnScreenOnly | kCGWindowListExcludeDesktopElements, kCGNullWindowID);
        if (!windows) return 0;
        int visible = 0;
        for (NSDictionary *window in (NSArray *)windows) {
            if ([window[(id)kCGWindowOwnerPID] intValue] == pid && [window[(id)kCGWindowLayer] intValue] == 0) {
                visible = 1;
                break;
            }
        }
        CFRelease(windows);
        return visible;
    }
}
*/
import "C"

func focusKeyPouchBrowserWindow(pid int) bool {
	return C.ant_focus_mac_browser(C.pid_t(pid)) != 0
}
