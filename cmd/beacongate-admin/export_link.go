package main

import (
	"flag"
	"fmt"
	"os"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/trustwall1337/beacongate/engine/config"
)

// exportLink encodes a client_config.json into a `bg://` share-link
// (engine/config/share_link.go). Optional flags emit a Unicode-block
// QR code on stdout (`--qr`) and/or a PNG file (`--qr-png FILE`).
//
// Usage:
//
//	beacongate-admin export-link --config client_config.json
//	beacongate-admin export-link --config client_config.json --qr
//	beacongate-admin export-link --config client_config.json --qr-png /tmp/handoff.png
//
// The link is the entire client configuration including the AES key.
// We print a sensitive-data warning to stderr after every export so
// the operator can't miss it.
func exportLink() {
	fs := flag.NewFlagSet("export-link", flag.ExitOnError)
	cfgPath := fs.String("config", "", "path to client config JSON to encode")
	wantQR := fs.Bool("qr", false, "print a Unicode-block QR code to stdout")
	qrPNG := fs.String("qr-png", "", "write the QR code as a PNG file at this path")
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

	// The link itself goes to stdout so it's pipe-friendly
	// (`beacongate-admin export-link ... | pbcopy`).
	fmt.Println(link)

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

	fmt.Fprintf(os.Stderr, "\n==> Link contents (no key shown): %s\n", config.LinkSafeSummary(cfg))
	fmt.Fprintln(os.Stderr, "==> WARNING: the link above contains the AES key.")
	fmt.Fprintln(os.Stderr, "==> Treat it like a password. Send via end-to-end-encrypted channel only.")
	fmt.Fprintln(os.Stderr, "==> Anyone with this link can use your tunnel/VPS as if they were you.")
}
