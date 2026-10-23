package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/iodesystems/raglit"
	flag "github.com/spf13/pflag"
)

// `raglit service login-admin` opens the review UI signed in as admin.
//
// A fresh install leaves two questions — where is it, and how do I get in —
// and two things know the answers: daemon.json (the running daemon's address)
// and the daemon itself (/api/health names its admin token file). The token
// travels in the URL FRAGMENT of /auth/admin, which the browser never sends;
// that page exchanges it for a session cookie and drops it from the address
// bar (authsession.go). It sits in the argv of open/xdg-open for a moment.
func serviceLoginAdmin(args []string) error {
	fs := flag.NewFlagSet("service login-admin", flag.ExitOnError)
	base := fs.String("url", "", "daemon base URL (default: the running daemon's address from daemon.json, else http://127.0.0.1:"+defaultDaemonPort+")")
	root := fs.String("root", "", "daemon storage root (default ~/.raglit or $RAGLIT_ROOT)")
	printIt := fs.Bool("print", false, "print the sign-in URL instead of opening it (it contains the admin token)")
	fs.Parse(args)

	if *root == "" {
		*root = raglit.DefaultRoot()
	}
	if *base == "" {
		addr := ""
		if st, ok := readDaemonState(*root); ok {
			addr = st.Addr
		}
		*base = localBase(addr)
	}
	*base = strings.TrimRight(*base, "/")

	var health struct {
		Auth       bool   `json:"auth"`
		AdminToken string `json:"admin_token"`
	}
	hc := &http.Client{Timeout: 5 * time.Second}
	if err := getJSONHealth(hc, *base+"/api/health", &health); err != nil {
		return fmt.Errorf("nothing answers at %s (%v) — is it running? `raglit service status`", *base, err)
	}
	target := *base + "/"
	if !health.Auth {
		fmt.Printf("%s does not authenticate (no auth.issuer in its config.json): no sign-in needed\n", *base)
	} else {
		b, err := os.ReadFile(health.AdminToken)
		if err != nil {
			return fmt.Errorf("read the admin token the daemon names (%s): %w — run this as the user the daemon runs as", health.AdminToken, err)
		}
		target = *base + "/auth/admin#token=" + url.QueryEscape(strings.TrimSpace(string(b)))
		fmt.Printf("admin token: %s\n", health.AdminToken)
	}
	if *printIt {
		fmt.Println(target)
		return nil
	}
	if err := openBrowser(target); err != nil {
		// No browser here (an ssh session): the URL is the answer.
		fmt.Printf("could not open a browser (%v); open this:\n%s\n", err, target)
		return nil
	}
	fmt.Printf("opened %s/ in the browser\n", *base)
	return nil
}

// localBase turns the daemon's listen list into a URL this machine's browser
// can open: the first bind, preferring a loopback one; all-interfaces and empty
// hosts become 127.0.0.1.
func localBase(addr string) string {
	if addr == "" {
		addr = "127.0.0.1:" + defaultDaemonPort
	}
	binds, err := parseListenList(addr, defaultDaemonPort)
	if err != nil || len(binds) == 0 {
		return "http://" + addr
	}
	pick := binds[0]
	for _, b := range binds {
		if h, _, err := net.SplitHostPort(b); err == nil {
			if ip := net.ParseIP(h); ip != nil && ip.IsLoopback() {
				pick = b
				break
			}
		}
	}
	host, port, err := net.SplitHostPort(pick)
	if err != nil {
		return "http://" + pick
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

func getJSONHealth(hc *http.Client, u string, v any) error {
	resp, err := hc.Get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", u, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func openBrowser(u string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", u).Run()
	default:
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return fmt.Errorf("no display")
		}
		return exec.Command("xdg-open", u).Run()
	}
}
