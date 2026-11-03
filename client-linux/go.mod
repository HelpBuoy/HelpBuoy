module github.com/mevdschee/helpbuoy/client-linux

go 1.26.0

require (
	github.com/jezek/xgb v1.3.1
	github.com/mevdschee/helpbuoy/client-windows v0.0.0
	github.com/mevdschee/helpbuoy/proto v0.0.0
	golang.org/x/image v0.47.0
	golang.org/x/sys v0.49.0
)

require (
	github.com/klauspost/compress v1.20.3 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)

replace (
	github.com/mevdschee/helpbuoy/client-windows => ../client-windows
	github.com/mevdschee/helpbuoy/proto => ../proto
)
