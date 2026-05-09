// Command beacongate-admin is a small CLI wrapper around the admin HTTP
// API. It also generates AEAD keys for first-time setup.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/trustwall1337/beacongate/engine/config"
	"github.com/trustwall1337/beacongate/engine/crypto"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "gen-key":
		genKey()
	case "list-rules":
		listRules()
	case "put-rule":
		putRule()
	case "delete-rule":
		deleteRule()
	case "status":
		serverStatus()
	case "migrate-config":
		migrateConfig()
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  beacongate-admin gen-key
  beacongate-admin list-rules    --addr URL [--token TOKEN]
  beacongate-admin put-rule      --addr URL [--token TOKEN] --file rule.json
  beacongate-admin delete-rule   --addr URL [--token TOKEN] --id ID
  beacongate-admin status        --addr URL [--token TOKEN]
  beacongate-admin migrate-config --file client_config.json [--dry-run]
                                  Rewrite a pre-v1.1 client config to the
                                  v1.1 shape (idempotent).`)
	os.Exit(2)
}

func genKey() {
	key, err := crypto.GenerateKey()
	if err != nil {
		die("gen-key: %v", err)
	}
	fmt.Println(config.EncodeKey(key))
}

func parseCommonFlags(name string) (*flag.FlagSet, *string, *string) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	addr := fs.String("addr", "http://127.0.0.1:9090", "admin API base URL")
	token := fs.String("token", "", "bearer token for remote admin")
	return fs, addr, token
}

func listRules() {
	fs, addr, token := parseCommonFlags("list-rules")
	fs.Parse(os.Args[2:])
	body := mustRequest(http.MethodGet, *addr+"/api/policy/rules", *token, nil)
	io.Copy(os.Stdout, bytes.NewReader(body))
	fmt.Println()
}

func putRule() {
	fs, addr, token := parseCommonFlags("put-rule")
	file := fs.String("file", "", "JSON file with rule body")
	fs.Parse(os.Args[2:])
	if *file == "" {
		die("--file is required")
	}
	data, err := os.ReadFile(*file)
	if err != nil {
		die("read: %v", err)
	}
	body := mustRequest(http.MethodPost, *addr+"/api/policy/rules", *token, data)
	io.Copy(os.Stdout, bytes.NewReader(body))
	fmt.Println()
}

func deleteRule() {
	fs, addr, token := parseCommonFlags("delete-rule")
	id := fs.String("id", "", "rule id")
	fs.Parse(os.Args[2:])
	if *id == "" {
		die("--id is required")
	}
	mustRequest(http.MethodDelete, *addr+"/api/policy/rules/"+*id, *token, nil)
	fmt.Printf("deleted %s\n", *id)
}

func serverStatus() {
	fs, addr, token := parseCommonFlags("status")
	fs.Parse(os.Args[2:])
	body := mustRequest(http.MethodGet, *addr+"/api/status", *token, nil)
	io.Copy(os.Stdout, bytes.NewReader(body))
	fmt.Println()
}

func mustRequest(method, url, token string, body []byte) []byte {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		die("request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		die("call: %v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		die("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(out)))
	}
	return out
}

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}

// Handle decode errors with a readable message even though we don't decode
// here — we forward raw JSON. This keeps the binary tiny.
var _ = json.Marshal
