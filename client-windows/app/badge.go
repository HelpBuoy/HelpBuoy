package app

import (
	"context"

	"golang.org/x/crypto/ssh"

	"github.com/helpbuoy/helpbuoy/client-windows/session"
	"github.com/helpbuoy/helpbuoy/proto/i18n"
	"github.com/helpbuoy/helpbuoy/proto/verify"
)

// Badges is what the approval dialog shows about the server for one request
// (PLAN.md 12.6). The check may still run: Badge then says so, and Done is
// closed once it finished.
type Badges struct {
	v    *session.Verification
	addr string
}

// Done is closed when the verified server check finished.
func (b *Badges) Done() <-chan struct{} { return b.v.Done() }

// Badge is the dialog's part about the server.
type Badge struct {
	// Verified is set when the server belongs to a company HelpBuoy checked.
	// Only then are Heading and Server set; the window shows them in their
	// own area, with their own icon and color, where no text from the server
	// is ever shown.
	Verified bool
	Heading  string // "Verified: Example Inc, Netherlands"
	Server   string // "Server: helpme.example.com"
	// Text explains the badge, or warns when the server is not verified.
	Text string
}

// Badge returns the badge for the check's result so far.
func (b *Badges) Badge() Badge {
	warn := i18n.Trans("Only allow this if you know the person asking personally. " +
		"Banks, Microsoft and the police never ask for this.")
	select {
	case <-b.v.Done():
	default:
		return Badge{Text: i18n.Trans("Checking who runs this server...") + " " + warn}
	}
	r := b.v.Result()
	switch r.State {
	case verify.Verified:
		host := verify.HostName(b.addr)
		heading := i18n.Trans("Verified: {0}", r.Company)
		if c := countryName(r.Country); c != "" {
			heading = i18n.Trans("Verified: {0}, {1}", r.Company, c)
		}
		return Badge{
			Verified: true,
			Heading:  heading,
			Server:   i18n.Trans("Server: {0}", host),
			Text:     i18n.Trans("HelpBuoy checked this company. It vouches for its support staff."),
		}
	case verify.NotVerified:
		return Badge{Text: i18n.Trans("HelpBuoy has not verified who runs this server.") + " " + warn}
	default:
		return Badge{Text: i18n.Trans("HelpBuoy could not check who runs this server.") + " " + warn}
	}
}

// countryName returns the name of an ISO 3166 country code in the app's
// language, or the code itself for a country not listed here.
func countryName(code string) string {
	switch code {
	case "":
		return ""
	case "AT":
		return i18n.Trans("Austria")
	case "BE":
		return i18n.Trans("Belgium")
	case "CH":
		return i18n.Trans("Switzerland")
	case "DE":
		return i18n.Trans("Germany")
	case "DK":
		return i18n.Trans("Denmark")
	case "ES":
		return i18n.Trans("Spain")
	case "FI":
		return i18n.Trans("Finland")
	case "FR":
		return i18n.Trans("France")
	case "GB":
		return i18n.Trans("United Kingdom")
	case "IE":
		return i18n.Trans("Ireland")
	case "IT":
		return i18n.Trans("Italy")
	case "LU":
		return i18n.Trans("Luxembourg")
	case "NL":
		return i18n.Trans("Netherlands")
	case "NO":
		return i18n.Trans("Norway")
	case "PL":
		return i18n.Trans("Poland")
	case "PT":
		return i18n.Trans("Portugal")
	case "SE":
		return i18n.Trans("Sweden")
	case "US":
		return i18n.Trans("United States")
	}
	return code
}

// NewVerifier returns the verified server check the clients use: helpbuoy.com
// over HTTPS, with version in the User-Agent, and the file must list the host
// name of the server address (PLAN.md 12.5).
func NewVerifier(version string) session.Verifier {
	c := verify.NewChecker(version, verify.Options{})
	return func(ctx context.Context, hostKey ssh.PublicKey, salt []byte, addr string) verify.Result {
		return c.Check(ctx, hostKey, salt, addr, true)
	}
}
