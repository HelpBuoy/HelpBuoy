module github.com/helpbuoy/helpbuoy/client-linux

go 1.26.0

require (
	github.com/godbus/dbus/v5 v5.2.2
	github.com/helpbuoy/helpbuoy/client-windows v0.0.0
	github.com/helpbuoy/helpbuoy/proto v0.0.0
	github.com/jezek/xgb v1.3.1
	golang.org/x/image v0.47.0
	golang.org/x/sys v0.49.0
)

require (
	github.com/coder/websocket v1.8.15 // indirect
	github.com/klauspost/compress v1.20.3 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)

replace (
	github.com/helpbuoy/helpbuoy/client-windows => ../client-windows
	github.com/helpbuoy/helpbuoy/proto => ../proto
)
