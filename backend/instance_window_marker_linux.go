//go:build linux && cgo

package backend

/*
#cgo pkg-config: xcb
#include <xcb/xcb.h>
#include <stdlib.h>
#include <string.h>

typedef struct {
    xcb_connection_t *connection;
    xcb_window_t root;
    xcb_atom_t clients, pid, title, utf8, motif, type, normal, i3socket;
} ant_marker_x11;

static xcb_atom_t ant_marker_atom(xcb_connection_t *connection, const char *name) {
    xcb_intern_atom_reply_t *reply = xcb_intern_atom_reply(connection,
        xcb_intern_atom(connection, 0, strlen(name), name), NULL);
    if (!reply) return XCB_ATOM_NONE;
    xcb_atom_t atom = reply->atom;
    free(reply);
    return atom;
}

static ant_marker_x11 *ant_marker_open(void) {
    int screen_number = 0;
    xcb_connection_t *connection = xcb_connect(NULL, &screen_number);
    if (!connection || xcb_connection_has_error(connection)) {
        if (connection) xcb_disconnect(connection);
        return NULL;
    }
    xcb_screen_iterator_t screens = xcb_setup_roots_iterator(xcb_get_setup(connection));
    for (int index = 0; index < screen_number && screens.rem; index++) xcb_screen_next(&screens);
    if (!screens.rem) { xcb_disconnect(connection); return NULL; }
    ant_marker_x11 *display = calloc(1, sizeof(ant_marker_x11));
    if (!display) { xcb_disconnect(connection); return NULL; }
    display->connection = connection;
    display->root = screens.data->root;
    display->clients = ant_marker_atom(connection, "_NET_CLIENT_LIST");
    display->pid = ant_marker_atom(connection, "_NET_WM_PID");
    display->title = ant_marker_atom(connection, "_NET_WM_NAME");
    display->utf8 = ant_marker_atom(connection, "UTF8_STRING");
    display->motif = ant_marker_atom(connection, "_MOTIF_WM_HINTS");
    display->type = ant_marker_atom(connection, "_NET_WM_WINDOW_TYPE");
    display->normal = ant_marker_atom(connection, "_NET_WM_WINDOW_TYPE_NORMAL");
    display->i3socket = ant_marker_atom(connection, "I3_SOCKET_PATH");
    return display;
}

static void ant_marker_close(ant_marker_x11 *display) {
    if (!display) return;
    xcb_disconnect(display->connection);
    free(display);
}

static xcb_get_property_reply_t *ant_marker_property(ant_marker_x11 *display, xcb_window_t window, xcb_atom_t atom) {
    return xcb_get_property_reply(display->connection,
        xcb_get_property(display->connection, 0, window, atom, XCB_GET_PROPERTY_TYPE_ANY, 0, 16384), NULL);
}

static char *ant_marker_string(ant_marker_x11 *display, xcb_window_t window, xcb_atom_t atom) {
    xcb_get_property_reply_t *reply = ant_marker_property(display, window, atom);
    if (!reply) return NULL;
    int length = xcb_get_property_value_length(reply);
    char *value = NULL;
    if (reply->format == 8 && length > 0) {
        value = malloc(length + 1);
        if (value) { memcpy(value, xcb_get_property_value(reply), length); value[length] = 0; }
    }
    free(reply);
    return value;
}

static char *ant_marker_title(ant_marker_x11 *display, xcb_window_t window) {
    char *title = ant_marker_string(display, window, display->title);
    if (!title) title = ant_marker_string(display, window, XCB_ATOM_WM_NAME);
    return title;
}

static char *ant_marker_i3socket(ant_marker_x11 *display) {
    return ant_marker_string(display, display->root, display->i3socket);
}

static uint32_t ant_marker_pid(ant_marker_x11 *display, xcb_window_t window) {
    xcb_get_property_reply_t *reply = ant_marker_property(display, window, display->pid);
    uint32_t pid = 0;
    if (reply && reply->format == 32 && xcb_get_property_value_length(reply) >= 4)
        memcpy(&pid, xcb_get_property_value(reply), 4);
    free(reply);
    return pid;
}

static int ant_marker_normal(ant_marker_x11 *display, xcb_window_t window) {
    xcb_get_property_reply_t *reply = ant_marker_property(display, window, display->type);
    int normal = 1;
    if (reply && reply->format == 32 && xcb_get_property_value_length(reply) >= 4) {
        uint32_t type = 0;
        memcpy(&type, xcb_get_property_value(reply), 4);
        normal = type == display->normal;
    }
    free(reply);
    return normal;
}

static xcb_window_t *ant_marker_windows(ant_marker_x11 *display, int *count) {
    *count = 0;
    xcb_get_property_reply_t *reply = ant_marker_property(display, display->root, display->clients);
    xcb_window_t *windows = NULL;
    if (reply && reply->format == 32 && xcb_get_property_value_length(reply) > 0) {
        int bytes = xcb_get_property_value_length(reply);
        windows = malloc(bytes);
        if (windows) { memcpy(windows, xcb_get_property_value(reply), bytes); *count = bytes / 4; }
    }
    free(reply);
    if (windows) return windows;
    // Xvfb and desktops without EWMH expose the browser directly below root.
    xcb_query_tree_reply_t *tree = xcb_query_tree_reply(display->connection,
        xcb_query_tree(display->connection, display->root), NULL);
    if (!tree) return NULL;
    int length = xcb_query_tree_children_length(tree);
    if (length > 0) {
        windows = malloc(length * sizeof(xcb_window_t));
        if (windows) { memcpy(windows, xcb_query_tree_children(tree), length * sizeof(xcb_window_t)); *count = length; }
    }
    free(tree);
    return windows;
}

// Return the topmost normal window for this exact browser PID. The window
// manager retains the selected tab. Without an EWMH manager (e.g. Xvfb),
// X11 itself owns stacking and input focus.
static xcb_window_t ant_focus_property_window(ant_marker_x11 *d, const char *name) {
    xcb_get_property_reply_t *reply = ant_marker_property(d, d->root, ant_marker_atom(d->connection, name));
    xcb_window_t value = XCB_NONE;
    if (reply && reply->format == 32 && xcb_get_property_value_length(reply) >= 4)
        memcpy(&value, xcb_get_property_value(reply), 4);
    free(reply);
    return value;
}

static xcb_window_t ant_focus_top_level(ant_marker_x11 *d) {
    xcb_get_input_focus_reply_t *reply = xcb_get_input_focus_reply(d->connection, xcb_get_input_focus(d->connection), NULL);
    xcb_window_t w = reply ? reply->focus : XCB_NONE;
    free(reply);
    while (w != XCB_NONE && w != XCB_INPUT_FOCUS_POINTER_ROOT && w != d->root) {
        xcb_query_tree_reply_t *tree = xcb_query_tree_reply(d->connection, xcb_query_tree(d->connection, w), NULL);
        if (!tree) return XCB_NONE;
        xcb_window_t parent = tree->parent;
        free(tree);
        if (parent == d->root) return w;
        w = parent;
    }
    return XCB_NONE;
}

static int ant_focus_browser_pid(ant_marker_x11 *d, uint32_t pid) {
    int managed = ant_focus_property_window(d, "_NET_SUPPORTING_WM_CHECK") != XCB_NONE;
    xcb_window_t active = managed ? ant_focus_property_window(d, "_NET_ACTIVE_WINDOW") : ant_focus_top_level(d);
    if (active != XCB_NONE && ant_marker_pid(d, active) == pid && ant_marker_normal(d, active)) return 1;
    // _NET_CLIENT_LIST_STACKING and QueryTree both order windows bottom to top.
    xcb_atom_t stacked = ant_marker_atom(d->connection, "_NET_CLIENT_LIST_STACKING");
    xcb_get_property_reply_t *order = ant_marker_property(d, d->root, stacked);
    int count = 0;
    xcb_window_t *windows = NULL, *fallback = NULL, target = XCB_NONE;
    if (order && order->format == 32 && xcb_get_property_value_length(order) >= 4) {
        windows = xcb_get_property_value(order);
        count = xcb_get_property_value_length(order) / 4;
    } else {
        windows = fallback = ant_marker_windows(d, &count);
    }
    for (int i = count-1; i >= 0; i--) {
        xcb_window_t w = windows[i];
        if (ant_marker_pid(d, w) != pid || !ant_marker_normal(d, w)) continue;
        xcb_get_window_attributes_reply_t *attr = xcb_get_window_attributes_reply(d->connection,
            xcb_get_window_attributes(d->connection, w), NULL);
        int eligible = attr && !attr->override_redirect && (managed || attr->map_state == XCB_MAP_STATE_VIEWABLE);
        free(attr);
        if (eligible) { target = w; break; }
    }
    free(order);
    free(fallback);
    if (target == XCB_NONE) return 0;
    if (managed) {
        xcb_client_message_event_t request = {0};
        request.response_type = XCB_CLIENT_MESSAGE;
        request.format = 32;
        request.type = ant_marker_atom(d->connection, "_NET_ACTIVE_WINDOW");
        request.window = target;
        request.data.data32[0] = 2; // user-initiated desktop activation
        request.data.data32[1] = XCB_CURRENT_TIME;
        request.data.data32[2] = active;
        xcb_send_event(d->connection, 0, d->root, XCB_EVENT_MASK_SUBSTRUCTURE_REDIRECT | XCB_EVENT_MASK_SUBSTRUCTURE_NOTIFY,
            (const char *)&request);
    } else {
        uint32_t above = XCB_STACK_MODE_ABOVE;
        xcb_configure_window(d->connection, target, XCB_CONFIG_WINDOW_STACK_MODE, &above);
        xcb_set_input_focus(d->connection, XCB_INPUT_FOCUS_PARENT, target, XCB_CURRENT_TIME);
    }
    xcb_flush(d->connection);
    active = managed ? ant_focus_property_window(d, "_NET_ACTIVE_WINDOW") : ant_focus_top_level(d);
    return active == target;
}

static int ant_marker_motif(ant_marker_x11 *display, xcb_window_t window, uint32_t *hints) {
    xcb_get_property_reply_t *reply = ant_marker_property(display, window, display->motif);
    int found = reply && reply->format == 32 && xcb_get_property_value_length(reply) >= 20;
    if (found) memcpy(hints, xcb_get_property_value(reply), 20);
    free(reply);
    return found;
}

static void ant_marker_set_motif(ant_marker_x11 *display, xcb_window_t window, uint32_t *hints, int present) {
    if (present) xcb_change_property(display->connection, XCB_PROP_MODE_REPLACE, window,
        display->motif, display->motif, 32, 5, hints);
    else xcb_delete_property(display->connection, window, display->motif);
}

static void ant_marker_set_title(ant_marker_x11 *display, xcb_window_t window, const char *title, int length) {
    xcb_change_property(display->connection, XCB_PROP_MODE_REPLACE, window,
        display->title, display->utf8, 8, length, title);
    xcb_change_property(display->connection, XCB_PROP_MODE_REPLACE, window,
        XCB_ATOM_WM_NAME, display->utf8, 8, length, title);
}
*/
import "C"

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"
	"unsafe"
)

type profileWindowMarkerLinuxWindow struct {
	pid         uint32
	motif       [5]C.uint32_t
	motifExists bool
	i3Border    string
	i3Width     int
	i3Applied   bool
}

func profileWindowMarkerSupported() bool {
	return strings.TrimSpace(os.Getenv("DISPLAY")) != ""
}

func focusKeyPouchBrowserWindow(pid int) bool {
	display := C.ant_marker_open()
	if display == nil {
		return false
	}
	defer C.ant_marker_close(display)
	return C.ant_focus_browser_pid(display, C.uint32_t(pid)) != 0
}

func profileWindowMarkerLinuxTitle(currentTitle, markerCode, profileName string) string {
	name := sanitizeProfileWindowMarkerName(profileName)
	if name == "" {
		name = "实例"
	}
	name = truncateProfileWindowMarkerName(name, 160)
	pageTitle := stripProfileWindowMarkerTitle(currentTitle)
	if pageTitle == "" {
		pageTitle = "Ant Browser"
	}
	return fmt.Sprintf("[%s] %s%s%s", markerCode, name, profileWindowMarkerSeparator, pageTitle)
}

func profileWindowMarkerLinuxReadTitle(display *C.ant_marker_x11, window C.xcb_window_t) string {
	value := C.ant_marker_title(display, window)
	if value == nil {
		return ""
	}
	defer C.free(unsafe.Pointer(value))
	return C.GoString(value)
}

func profileWindowMarkerLinuxSetTitle(display *C.ant_marker_x11, window C.xcb_window_t, title string) {
	value := C.CString(title)
	defer C.free(unsafe.Pointer(value))
	C.ant_marker_set_title(display, window, value, C.int(len(title)))
}

func runProfileWindowMarker(app *App, marker *profileWindowMarker) {
	defer app.releaseProfileWindowMarker(marker.profileID, marker)
	display := C.ant_marker_open()
	if display == nil {
		return
	}
	defer C.ant_marker_close(display)

	// Obtain i3's socket from this X server, never the inherited I3SOCK. This
	// prevents an isolated/test display from modifying the user's real desktop.
	i3Socket := ""
	if socket := C.ant_marker_i3socket(display); socket != nil {
		i3Socket = C.GoString(socket)
		C.free(unsafe.Pointer(socket))
	}
	windows := make(map[C.xcb_window_t]profileWindowMarkerLinuxWindow)
	processIDs := make(map[uint32]struct{})
	nextProcessScan := time.Time{}
	defer func() {
		for window, state := range windows {
			if uint32(C.ant_marker_pid(display, window)) != state.pid {
				continue
			}
			title := profileWindowMarkerLinuxReadTitle(display, window)
			unmarked := stripProfileWindowMarkerTitle(title)
			if title != unmarked {
				profileWindowMarkerLinuxSetTitle(display, window, unmarked)
			}
			present := C.int(0)
			if state.motifExists {
				present = 1
			}
			C.ant_marker_set_motif(display, window, &state.motif[0], present)
			if state.i3Applied {
				profileWindowMarkerI3SetBorder(i3Socket, uint32(window), state.i3Border, state.i3Width)
			}
		}
		C.xcb_flush(display.connection)
	}()

	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-marker.stop:
			return
		default:
		}
		target, ok := app.currentProfileWindowMarkerTarget(marker.profileID)
		if !ok || C.xcb_connection_has_error(display.connection) != 0 {
			return
		}
		if target.pid > 0 {
			processIDs[uint32(target.pid)] = struct{}{}
		}
		if len(processIDs) == 0 && time.Now().After(nextProcessScan) {
			processIDs = profileWindowMarkerLinuxProcessIDs(target)
			nextProcessScan = time.Now().Add(2 * time.Second)
		}
		matchedWindows := 0
		var count C.int
		list := C.ant_marker_windows(display, &count)
		if list != nil {
			for _, window := range unsafe.Slice(list, int(count)) {
				pid := uint32(C.ant_marker_pid(display, window))
				if _, matched := processIDs[pid]; !matched || C.ant_marker_normal(display, window) == 0 {
					continue
				}
				matchedWindows++
				state, exists := windows[window]
				if !exists || state.pid != pid {
					state = profileWindowMarkerLinuxWindow{pid: pid}
					state.motifExists = C.ant_marker_motif(display, window, &state.motif[0]) != 0
					if border, width, found := profileWindowMarkerI3Border(i3Socket, uint32(window)); found {
						state.i3Border, state.i3Width = border, width
						if border != "normal" {
							state.i3Applied = profileWindowMarkerI3SetBorder(i3Socket, uint32(window), "normal", 1)
						}
					}
					windows[window] = state
				}
				var hints [5]C.uint32_t
				present := C.ant_marker_motif(display, window, &hints[0]) != 0
				if !present || hints[0]&2 == 0 || hints[2] != 1 {
					hints[0] |= 2 // MWM_HINTS_DECORATIONS
					hints[2] = 1  // MWM_DECOR_ALL: let the window manager render the title bar.
					C.ant_marker_set_motif(display, window, &hints[0], 1)
				}
				title := profileWindowMarkerLinuxReadTitle(display, window)
				marked := profileWindowMarkerLinuxTitle(title, marker.code, target.profileName)
				if title != marked {
					profileWindowMarkerLinuxSetTitle(display, window, marked)
				}
			}
			C.free(unsafe.Pointer(list))
		}
		C.xcb_flush(display.connection)
		if matchedWindows == 0 && time.Now().After(nextProcessScan) {
			processIDs = profileWindowMarkerLinuxProcessIDs(target)
			nextProcessScan = time.Now().Add(2 * time.Second)
		}
		select {
		case <-marker.stop:
			return
		case <-ticker.C:
		}
	}
}

func profileWindowMarkerLinuxProcessIDs(target profileWindowMarkerTarget) map[uint32]struct{} {
	processIDs := make(map[uint32]struct{})
	if target.pid > 0 {
		processIDs[uint32(target.pid)] = struct{}{}
	}
	// Recovered instances can expose a valid CDP port without a stored PID.
	// Resolve only browser processes for this exact profile directory.
	processes, err := findBrowserUserDataProcessesOS(target.userDataDir)
	if err != nil {
		return processIDs
	}
	for _, process := range processes {
		if process.PID <= 0 || (target.debugPort > 0 && process.DebugPort > 0 && target.debugPort != process.DebugPort) {
			continue
		}
		args := splitProcessCommandLine(process.CommandLine)
		if processCommandLineFlagValue(args, "--type") != "" {
			continue
		}
		processIDs[uint32(process.PID)] = struct{}{}
	}
	return processIDs
}

// i3 deliberately ignores application decoration hints for tiled windows. Its
// IPC lets us request a real top title bar for exactly the matched browser XID.
func profileWindowMarkerI3Request(socket string, messageType uint32, payload string) ([]byte, error) {
	if socket == "" {
		return nil, fmt.Errorf("not an i3 display")
	}
	connection, err := net.DialTimeout("unix", socket, 300*time.Millisecond)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(500 * time.Millisecond))
	header := make([]byte, 14)
	copy(header, "i3-ipc")
	binary.LittleEndian.PutUint32(header[6:10], uint32(len(payload)))
	binary.LittleEndian.PutUint32(header[10:14], messageType)
	if _, err = io.Copy(connection, strings.NewReader(string(header)+payload)); err != nil {
		return nil, err
	}
	if _, err = io.ReadFull(connection, header); err != nil {
		return nil, err
	}
	length := binary.LittleEndian.Uint32(header[6:10])
	if string(header[:6]) != "i3-ipc" || binary.LittleEndian.Uint32(header[10:14]) != messageType || length > 4*1024*1024 {
		return nil, fmt.Errorf("invalid i3 response")
	}
	response := make([]byte, int(length))
	_, err = io.ReadFull(connection, response)
	return response, err
}

type profileWindowMarkerI3Node struct {
	Window        uint32                      `json:"window"`
	Border        string                      `json:"border"`
	Width         int                         `json:"current_border_width"`
	Nodes         []profileWindowMarkerI3Node `json:"nodes"`
	FloatingNodes []profileWindowMarkerI3Node `json:"floating_nodes"`
}

func (node profileWindowMarkerI3Node) findBorder(window uint32) (string, int, bool) {
	if node.Window == window {
		switch node.Border {
		case "normal", "pixel", "none":
			return node.Border, node.Width, true
		}
	}
	for _, children := range [][]profileWindowMarkerI3Node{node.Nodes, node.FloatingNodes} {
		for _, child := range children {
			if border, width, found := child.findBorder(window); found {
				return border, width, found
			}
		}
	}
	return "", 0, false
}

func profileWindowMarkerI3Border(socket string, window uint32) (string, int, bool) {
	response, err := profileWindowMarkerI3Request(socket, 4, "") // IPC_GET_TREE
	if err != nil {
		return "", 0, false
	}
	var tree profileWindowMarkerI3Node
	if json.Unmarshal(response, &tree) != nil {
		return "", 0, false
	}
	return tree.findBorder(window)
}

func profileWindowMarkerI3SetBorder(socket string, window uint32, border string, width int) bool {
	if window == 0 {
		return false
	}
	switch border {
	case "normal", "pixel", "none":
	default:
		return false
	}
	if width < 0 || width > 100 {
		width = 1
	}
	payload := fmt.Sprintf(`[id="%d"] border %s %d`, window, border, width)
	response, err := profileWindowMarkerI3Request(socket, 0, payload)
	if err != nil {
		return false
	}
	var results []struct {
		Success bool `json:"success"`
	}
	return json.Unmarshal(response, &results) == nil && len(results) == 1 && results[0].Success
}
