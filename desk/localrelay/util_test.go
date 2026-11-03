package localrelay

import (
	"os"
	"strconv"
)

func itoa(i int) string { return strconv.Itoa(i) }

func writeFile(p string) error { return os.WriteFile(p, []byte("x"), 0o600) }
