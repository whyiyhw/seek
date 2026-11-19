package opencodeauth

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"
)

// Run is `seek auth opencode [flags]`. Dispatched before flag.Parse (same
// rule as every other subcommand: no provider probe, no session, no
// project), so it parses its own flags from args.
func Run(args []string, out, errW io.Writer) error {
	if len(args) == 0 || args[0] != "opencode" {
		fmt.Fprintln(errW, "usage: seek auth opencode [--status|--logout] [--org <workspace-id>] [--no-browser]")
		fmt.Fprintln(errW, "       Log in to OpenCode Console in the browser and store the token at ~/.seek/opencode-auth.json.")
		if len(args) > 0 {
			fmt.Fprintf(errW, "unknown auth target %q (only \"opencode\" is served)\n", args[0])
		}
		return errors.New("auth: usage")
	}

	fs := flag.NewFlagSet("auth opencode", flag.ContinueOnError)
	fs.SetOutput(errW)
	status := fs.Bool("status", false, "show the stored login without changing it")
	logout := fs.Bool("logout", false, "revoke the stored token and delete it")
	noBrowser := fs.Bool("no-browser", false, "print the approval URL without opening a browser")
	org := fs.String("org", "", "workspace id to bill (required when the account has several)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	c := New()
	c.Out = out
	if *noBrowser {
		c.OpenBrowser = nil
	}

	switch {
	case *status:
		return printStatus(out, c)
	case *logout:
		return doLogout(context.Background(), c, out)
	}

	cred, _ := Load()
	cred.OrgID = *org // fillOrg validates membership
	fmt.Fprintf(out, "  Registering seek as an OAuth client (once)…\n")
	cred, err := c.Login(context.Background(), cred)
	if err != nil {
		fmt.Fprintln(errW, err)
		return err
	}
	if err := Save(cred); err != nil {
		return err
	}
	fmt.Fprintf(out, "  Logged in: workspace %q (%s), token valid until %s\n",
		cred.OrgName, cred.OrgID, cred.ExpiresAt.Local().Format(time.RFC1123))
	fmt.Fprintf(out, "  Stored at %s (0600). Revoke with `seek auth opencode --logout`.\n", mustPath())
	return nil
}

func mustPath() string {
	p, err := Path()
	if err != nil {
		return "~/.seek/opencode-auth.json"
	}
	return p
}

func printStatus(out io.Writer, c *Client) error {
	cred, ok := Load()
	if !ok || !cred.Valid() {
		fmt.Fprintln(out, "  Not logged in. Run `seek auth opencode`.")
		return nil
	}
	state := "valid"
	if c.now().After(cred.ExpiresAt) {
		state = "expired (next use refreshes it)"
	}
	fmt.Fprintf(out, "  Client:    %s (%s)\n", cred.ClientName, cred.ClientID)
	fmt.Fprintf(out, "  Workspace: %s (%s)\n", cred.OrgName, cred.OrgID)
	fmt.Fprintf(out, "  Token:     %s, expires %s\n", state, cred.ExpiresAt.Local().Format(time.RFC1123))
	fmt.Fprintf(out, "  File:      %s\n", mustPath())
	return nil
}

func doLogout(ctx context.Context, c *Client, out io.Writer) error {
	cred, ok := Load()
	if !ok {
		fmt.Fprintln(out, "  Not logged in — nothing to revoke.")
		return nil
	}
	if err := c.Revoke(ctx, cred); err != nil {
		// The local file still goes: leaving a dead-but-stored token
		// around is worse than the network hiccup that missed the revoke.
		fmt.Fprintf(out, "  Warning: server-side revoke failed (%v); deleting the local token anyway.\n", err)
	}
	if err := Clear(); err != nil {
		return err
	}
	fmt.Fprintln(out, "  Logged out. The stored token is deleted.")
	return nil
}
