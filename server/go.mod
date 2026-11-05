module github.com/helpbuoy/helpbuoy/server

go 1.26.0

require (
	github.com/helpbuoy/helpbuoy/proto v0.0.0
	golang.org/x/crypto v0.58.0
)

require golang.org/x/sys v0.49.0 // indirect

replace github.com/helpbuoy/helpbuoy/proto => ../proto
