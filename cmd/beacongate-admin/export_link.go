package main

import (
	"flag"
	"fmt"
	"os"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/trustwall1337/beacongate/engine/config"
)

// defaultInstallerURL is the location the QR-encoded one-paste install
// command pulls the Termux installer from. Override per-invocation via
// `--installer-url` for forks / pinned tags / private mirrors.
const defaultInstallerURL = "https://raw.githubusercontent.com/trustwall1337/beacongate/master/scripts/termux-install.sh"

// exportLink encodes a client_config.json into a `bg://` share-link
// (engine/config/share_link.go). Optional flags emit a Unicode-block
// QR code on stdout (`--qr`) and/or a PNG file (`--qr-png FILE`).
//
// For the simplified Termux end-user flow ("paste one command"), use
// `--install-cmd`, `--install-qr`, or `--install-qr-png`. These wrap
// the bg:// link in a `curl ... | bash -s -- --import "..."` command
// targeting `scripts/termux-install.sh`. The friend scans the QR with
// their phone camera, copies the resulting text, and pastes it into
// Termux — one paste replaces the bundle's ~6-step manual sequence.
//
// Usage:
//
//	beacongate-admin export-link --config client_config.json
//	beacongate-admin export-link --config client_config.json --qr
//	beacongate-admin export-link --config client_config.json --qr-png /tmp/handoff.png
//	beacongate-admin export-link --config client_config.json --install-cmd
//	beacongate-admin export-link --config client_config.json --install-qr
//	beacongate-admin export-link --config client_config.json --install-qr-png /tmp/install-qr.png
//
// The link is the entire client configuration including the AES key.
// We print a sensitive-data warning to stderr after every export so
// the operator can't miss it.
func exportLink() {
	fs := flag.NewFlagSet("export-link", flag.ExitOnError)
	cfgPath := fs.String("config", "", "path to client config JSON to encode")
	wantQR := fs.Bool("qr", false, "print a Unicode-block QR code of the bg:// link to stdout")
	qrPNG := fs.String("qr-png", "", "write the QR code as a PNG file at this path")
	wantInstallCmd := fs.Bool("install-cmd", false, "print a one-paste Termux install command (curl|bash with --import) to stdout instead of the bare bg:// link")
	wantInstallQR := fs.Bool("install-qr", false, "print a Unicode-block QR code of the install command to stdout")
	installQRPNG := fs.String("install-qr-png", "", "write the install-command QR as a PNG file at this path")
	installerURL := fs.String("installer-url", defaultInstallerURL, "URL the install command pulls the Termux installer from")
	_ = fs.Parse(os.Args[2:])

	if *cfgPath == "" {
		die("export-link: --config is required")
	}
	cfg, err := config.LoadClient(*cfgPath)
	if err != nil {
		die("export-link: load %s: %v", *cfgPath, err)
	}
	link, err := config.EncodeLink(cfg)
	if err != nil {
		die("export-link: encode: %v", err)
	}

	// installCmd is the full one-paste line the friend will run inside
	// Termux. Everything but the bg:// payload is fixed-shape, so this
	// composes cleanly. Single-quoted bg:// URL because the link itself
	// never contains a single quote (it's base64url + ASCII letters).
	installCmd := fmt.Sprintf(
		`curl -fsSL %s | bash -s -- --import '%s'`,
		*installerURL, link)

	// Default to printing the bare link unless the operator asked for
	// the install variant. Only one form goes to stdout so pipelines
	// (`... | pbcopy`) are unambiguous.
	if *wantInstallCmd {
		fmt.Println(installCmd)
	} else {
		fmt.Println(link)
	}

	if *wantQR {
		qr, err := qrcode.New(link, qrcode.Medium)
		if err != nil {
			die("export-link: qr generate: %v", err)
		}
		// ToString(true) uses the inverted color scheme that scans
		// reliably on most phone cameras when displayed in a dark
		// terminal. We add a blank line above so the QR doesn't
		// abut the link text on stdout.
		fmt.Fprintln(os.Stderr)
		fmt.Fprint(os.Stderr, qr.ToString(true))
	}

	if *qrPNG != "" {
		if err := qrcode.WriteFile(link, qrcode.Medium, 512, *qrPNG); err != nil {
			die("export-link: write qr png %q: %v", *qrPNG, err)
		}
		fmt.Fprintf(os.Stderr, "qr png written to %s\n", *qrPNG)
	}

	if *wantInstallQR {
		// Install-command QR is materially longer than the bare link
		// (curl prefix adds ~150 chars). Use Low correction to keep
		// the module count reasonable; phone cameras still decode it
		// reliably under indoor lighting.
		qr, err := qrcode.New(installCmd, qrcode.Low)
		if err != nil {
			die("export-link: install-qr generate: %v", err)
		}
		fmt.Fprintln(os.Stderr)
		fmt.Fprint(os.Stderr, qr.ToString(true))
	}

	if *installQRPNG != "" {
		// PNG output uses Medium correction; image size scales fine.
		if err := qrcode.WriteFile(installCmd, qrcode.Medium, 512, *installQRPNG); err != nil {
			die("export-link: write install-qr png %q: %v", *installQRPNG, err)
		}
		fmt.Fprintf(os.Stderr, "install-cmd qr png written to %s\n", *installQRPNG)
	}

	fmt.Fprintf(os.Stderr, "\n==> Link contents (no key shown): %s\n", config.LinkSafeSummary(cfg))
	fmt.Fprintln(os.Stderr, "==> WARNING: the link above contains the AES key.")
	fmt.Fprintln(os.Stderr, "==> Treat it like a password. Send via end-to-end-encrypted channel only.")
	fmt.Fprintln(os.Stderr, "==> Anyone with this link can use your tunnel/VPS as if they were you.")
}
