module github.com/helpbuoy/helpbuoy/desk

go 1.26.0

require (
	github.com/ebitengine/purego v0.10.0
	github.com/gen2brain/raylib-go/raylib v0.60.1
	golang.org/x/crypto v0.58.0
	golang.org/x/image v0.47.0
)

require (
	github.com/coder/websocket v1.8.15 // indirect
	github.com/klauspost/compress v1.20.3 // indirect
	golang.org/x/sys v0.49.0 // indirect
)

require (
	github.com/helpbuoy/helpbuoy/proto v0.0.0
	github.com/helpbuoy/helpbuoy/server v0.0.0
	github.com/jupiterrider/ffi v0.7.0 // indirect
	golang.org/x/exp v0.0.0-20260508232706-74f9aab9d74a // indirect
)

replace github.com/helpbuoy/helpbuoy/proto => ../proto

replace github.com/helpbuoy/helpbuoy/server => ../server
