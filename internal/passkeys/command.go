package passkeys

import (
	"fmt"
	"io"
	"strings"

	"github.com/aakarim/go-openlore/pkg/shell/cmds"
)

const passkeyHelp = `
  passkey — Manage WebAuthn passkeys for HTTP documentation access

  Passkeys let humans browse your lore in a web browser, authenticated
  via WebAuthn (Face ID, Touch ID, security keys). Credentials are
  stored in a plain JSON file that agents can edit directly.

  Usage:
    passkey register [options]    Start passkey registration
    passkey list                  List registered passkeys
    passkey revoke <name>         Remove a passkey by name
    passkey help                  Show this help

  Register options:
    --identity NAME  Identity to authenticate as (required). Must be an
                     identity in your auth config; login mints tokens for it.
    --name LABEL     Device label for the passkey (default: "default")

  Examples:
    passkey register --identity alice                 Register for identity "alice"
    passkey register --identity alice --name MacBook  Label the device
    passkey list                                      Show all passkeys
    passkey revoke "MacBook"                          Delete a passkey by label

  Setup:
    Passkeys are enabled by default. Registration links use the server's
    advertised HTTP address: external_url in openlore.yml, else the first
    passkeys.rp_origins entry, else http://localhost:<http_port>. To pin
    the relying party explicitly (rp_origins must include external_url
    when both are set):

      passkeys:
        enabled: true
        rp_id: docs.example.com             # your domain
        rp_origins: ["https://docs.example.com"]
        lore_path: /lore

    Then run 'passkey register' and open the printed URL in a browser.
    After registering, visit /lore/ to browse docs with your passkey.
`

// RegisterShellCommand registers the `passkey` shell command backed by the
// given Passkeys instance. baseURL is the public HTTP origin used to build the
// registration link printed to the agent.
func RegisterShellCommand(pk *Passkeys, baseURL string) {
	baseURL = strings.TrimRight(baseURL, "/")
	cmds.Register("passkey", func(ctx cmds.CmdContext, args []string, w io.Writer, errW io.Writer, stdin io.Reader) int {
		if len(args) == 0 {
			fmt.Fprint(w, passkeyHelp)
			return 0
		}

		switch args[0] {
		case "help", "-h", "--help":
			fmt.Fprint(w, passkeyHelp)
			return 0
		case "register":
			return pk.cmdRegister(baseURL, args[1:], w, errW)
		case "list":
			return pk.cmdList(w)
		case "revoke":
			return pk.cmdRevoke(args[1:], w, errW)
		default:
			fmt.Fprintf(errW, "passkey: unknown subcommand %q\n", args[0])
			fmt.Fprintln(errW, "Run 'passkey help' for usage.")
			return 1
		}
	})
}

func (pk *Passkeys) cmdRegister(baseURL string, args []string, w io.Writer, errW io.Writer) int {
	name := "default"
	identity := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--name":
			if i+1 < len(args) {
				name = args[i+1]
				i++
			}
		case "--identity":
			if i+1 < len(args) {
				identity = args[i+1]
				i++
			}
		}
	}

	if identity == "" {
		fmt.Fprintln(errW, "passkey: --identity <name> is required")
		fmt.Fprintln(errW, "Run 'passkey help' for usage.")
		return 1
	}
	// Validate the identity exists in the auth table so login can resolve its
	// authority. Skipped only when no token issuer is wired (standalone / no
	// auth config), where any name is accepted.
	if pk.tokens != nil && !pk.tokens.IdentityExists(identity) {
		fmt.Fprintf(errW, "passkey: unknown identity %q (not in auth config)\n", identity)
		return 1
	}

	pr, err := pk.pending.Create(identity, name)
	if err != nil {
		fmt.Fprintf(errW, "passkey: %s\n", err)
		return 1
	}

	url := fmt.Sprintf("%s/passkey/r/%s", baseURL, pr.Token)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  📱 Passkey Registration")
	fmt.Fprintln(w, "  ─────────────────────────")
	fmt.Fprintf(w, "  Device:    %s\n", name)
	fmt.Fprintf(w, "  Identity:  %s\n", identity)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  Visit this URL to register your passkey:")
	fmt.Fprintf(w, "  %s\n", url)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  ⏱  This link expires in 5 minutes.")
	fmt.Fprintln(w)
	return 0
}

func (pk *Passkeys) cmdList(w io.Writer) int {
	creds := pk.store.AllCredentials()
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  %-20s %-16s %s\n", "DEVICE", "IDENTITY", "REGISTERED")
	for _, c := range creds {
		fmt.Fprintf(w, "  %-20s %-16s %s\n", c.Name, c.Identity, c.CreatedAt.Format("2006-01-02 15:04"))
	}
	fmt.Fprintln(w)
	return 0
}

func (pk *Passkeys) cmdRevoke(args []string, w io.Writer, errW io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(errW, "passkey: revoke requires a name")
		return 1
	}
	name := args[0]
	removed, err := pk.store.Remove(name)
	if err != nil {
		fmt.Fprintf(errW, "passkey: %s\n", err)
		return 1
	}
	if !removed {
		fmt.Fprintf(errW, "passkey: no passkey named %q\n", name)
		return 1
	}
	fmt.Fprintf(w, "Revoked passkey %q\n", name)
	return 0
}
