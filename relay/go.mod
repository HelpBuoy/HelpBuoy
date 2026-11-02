module github.com/mevdschee/help-me-win/relay

go 1.26.0

require (
	github.com/mevdschee/help-me-win/proto v0.0.0
	golang.org/x/crypto v0.58.0
)

require golang.org/x/sys v0.49.0 // indirect

replace github.com/mevdschee/help-me-win/proto => ../proto
