// The C side of the injector: CGEvent posting and the local Esc tap.

#include <stdint.h>

// hb_magic marks the injector's events in kCGEventSourceUserData, so the
// Esc tap can tell them from the local keyboard.
#define HB_MAGIC 0x48656c70 // "Help"

int hb_inject_open(void);
void hb_inject_close(void);
int hb_inject_key(uint16_t code, int down, uint64_t flags, int modifier);
int hb_inject_mouse(uint32_t type, uint32_t button, double x, double y, int64_t clicks, uint64_t flags);
int hb_inject_scroll(int32_t dy, int32_t dx, uint64_t flags);
int hb_inject_text(const uint16_t *utf16, int n);
double hb_double_click(void);
void hb_main_display(double *x, double *y, double *w, double *h);

// hb_esc_create creates the Esc tap on the calling thread's run loop; it
// fails without the Accessibility permission. hb_esc_run runs that loop
// until hb_esc_stop.
int hb_esc_create(void);
void hb_esc_run(void);
void hb_esc_stop(void);
