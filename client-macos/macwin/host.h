// The C side of the AppKit host. Every function may be called from any
// thread; the work runs on the main thread. Events go to hbEvent, on the
// main thread.

#include <stdint.h>

enum {
	hbEvExpose = 1,
	hbEvFocus,  // a: 1 in, 0 out
	hbEvClose,
	hbEvPaste,  // text
	hbEvKey,    // a: ui.Key, b: flags, c: char, text
	hbEvMouse,  // a: ui.MouseKind, b: x, c: y (pixels), d: drag
};

enum {
	hbKeyCmd = 1,
	hbKeyWord = 2,
	hbKeyShift = 4,
	hbKeyRepeat = 8,
};

// Key values of ui.Key.
enum {
	hbKeyNone = 0,
	hbKeyEnter,
	hbKeyEscape,
	hbKeyTab,
	hbKeyBackTab,
	hbKeyBackspace,
	hbKeyDelete,
	hbKeyLeft,
	hbKeyRight,
	hbKeyHome,
	hbKeyEnd,
	hbKeyInsert,
};

void hb_app_init(const char *hide, const char *quit, const char *minimize, const uint8_t *icon, int iconLen);
void hb_app_run(void);
void hb_app_quit(void);
int hb_show_running(int pid);
int hb_win_open(const char *title, int w, int h, double *scale);
void hb_win_show(void);
void hb_win_put(const uint8_t *rgba, int w, int h, int stride);
void hb_win_title(const char *title);
void hb_win_minimize(void);
void hb_win_activate(void);
void hb_win_attention(int on);
void hb_win_cursor(int kind);
void hb_win_close(void);
void hb_clip_set(const char *text);
void hb_clip_request(void);
