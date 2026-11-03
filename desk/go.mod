module github.com/mevdschee/helpbuoy/desk

go 1.26.0

require (
	github.com/gen2brain/raylib-go/raylib v0.60.1
	golang.org/x/crypto v0.58.0
	golang.org/x/image v0.47.0
)

require (
	github.com/klauspost/compress v1.20.3 // indirect
	golang.org/x/sys v0.49.0 // indirect
)

require (
	github.com/ebitengine/purego v0.10.0 // indirect
	github.com/jupiterrider/ffi v0.7.0 // indirect
	github.com/mevdschee/helpbuoy/proto v0.0.0
	github.com/mevdschee/helpbuoy/server v0.0.0
	golang.org/x/exp v0.0.0-20260508232706-74f9aab9d74a // indirect
)

replace github.com/mevdschee/helpbuoy/proto => ../proto

replace github.com/mevdschee/helpbuoy/server => ../server
