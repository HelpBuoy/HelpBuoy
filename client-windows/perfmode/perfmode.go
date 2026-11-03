// Package perfmode is the optional "performance mode" of a session (PLAN.md
// 5.2), like the experience settings of RDP: while an agent is connected the
// desktop wallpaper is replaced by the solid background color and window
// animations and full-window dragging are turned off, so less of the screen
// changes. Font smoothing is never touched. The changes are made for the
// current logon only (never written to the user profile), so even if the
// client dies before restoring them they are gone at the next logon.
package perfmode
