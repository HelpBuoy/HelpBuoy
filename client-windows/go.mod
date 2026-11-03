module github.com/mevdschee/helpbuoy/client-windows

go 1.26.0

require (
	github.com/mevdschee/helpbuoy/proto v0.0.0
	github.com/rodrigocfd/windigo v0.2.7
)

require github.com/klauspost/compress v1.20.3 // indirect

require (
	golang.org/x/crypto v0.58.0
	golang.org/x/sys v0.49.0
)

replace github.com/mevdschee/helpbuoy/proto => ../proto
