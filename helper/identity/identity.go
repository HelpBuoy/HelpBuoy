// Package identity loads the helper app's own application-specific key
// (PLAN.md 7.1) through proto/appkey, in the data directory "helpbuoy".
package identity

import (
	"os"
	"os/user"
	"strings"

	"github.com/mevdschee/helpbuoy/proto/appkey"
	"github.com/mevdschee/helpbuoy/proto/datadir"
	"github.com/mevdschee/helpbuoy/proto/identity"
)

// AppName is the data directory name shared with the client app's layout.
const AppName = "helpbuoy"

// LegacyAppName is the data directory name before the rename to HelpBuoy;
// Dir moves it to AppName on first start.
const LegacyAppName = "help-me-win"

// Dir returns the helper's data directory. It does not create it.
func Dir() (string, error) { return datadir.Migrate(AppName, LegacyAppName) }

// Load loads the app key from dir, creating it on the very first start with
// the comment user@machine. created reports whether a new key was generated.
// A damaged key is returned as an error wrapping appkey.ErrDamaged; it is
// never replaced automatically.
func Load(dir string) (k *appkey.Key, created bool, err error) {
	return appkey.LoadOrCreate(dir, UserMachine())
}

// Remove deletes the app key. Call it only after the user confirmed.
func Remove(dir string) error { return appkey.Remove(dir) }

// UserMachine returns the current user@machine, the comment of a new key.
func UserMachine() string {
	if s := identity.UserMachine(); s != "" {
		return s
	}
	host, _ := os.Hostname()
	if i := strings.IndexByte(host, '.'); i > 0 {
		host = host[:i]
	}
	return userName() + "@" + host
}

// HelperName returns a name for this helper on a local relay: the lower-cased
// user part of user@machine, or "helper" if that is not a valid name.
func HelperName() string {
	um := UserMachine()
	name := um
	if i := strings.IndexByte(um, '@'); i >= 0 {
		name = um[:i]
	}
	name = strings.ToLower(name)
	if !validName(name) {
		return "helper"
	}
	return name
}

// validName is a local, conservative check: ASCII letters, digits and ._-.
func validName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		ok := r == '.' || r == '_' || r == '-' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z')
		if !ok {
			return false
		}
	}
	return true
}

func userName() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		name := u.Username
		if i := strings.LastIndexAny(name, `\/`); i >= 0 { // DOMAIN\user on Windows
			name = name[i+1:]
		}
		return strings.ToLower(name)
	}
	for _, v := range []string{"USER", "USERNAME"} {
		if s := os.Getenv(v); s != "" {
			return strings.ToLower(s)
		}
	}
	return "helper"
}
