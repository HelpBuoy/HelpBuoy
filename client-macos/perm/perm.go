// Package perm checks and asks for the macOS privacy permissions the
// client needs: Screen Recording to share the screen and Accessibility to
// inject input and watch for the local Esc. macOS ties both to the app's
// code signature, and a granted Screen Recording only counts after the app
// restarts.
package perm
