package views

import "github.com/gen2brain/beeep"

// notifyIcon is the PNG shown in notifications; set by the app at start.
var notifyIcon []byte

// SetNotifyIcon sets the app icon used for notifications.
func SetNotifyIcon(png []byte) {
	notifyIcon = png
	beeep.AppName = "help-me-win helper"
}

// notify shows an OS notification for a new help request. It runs in the
// background because some platforms shell out.
func notify(client string) {
	icon := any("")
	if notifyIcon != nil {
		icon = notifyIcon
	}
	go beeep.Notify("Help request", client+" asks for help.", icon)
}
