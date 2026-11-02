// Package deps pins dependencies that proto packages are about to use, so
// go.mod stays stable while several people work on proto at once. Remove an
// import here once a real package imports it.
package deps

import (
	_ "github.com/klauspost/compress/zstd"
	_ "golang.org/x/image/font/gofont/gomono"
	_ "golang.org/x/image/font/opentype"
)
