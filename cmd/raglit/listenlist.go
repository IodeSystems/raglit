package main

import (
	"fmt"
	"net"
	"strings"
)

// Binding a list of addresses instead of one.
//
// `--addr` took a single host:port, which forces a choice nobody should have to
// make: loopback and the daemon is invisible from the laptop, 0.0.0.0 and every
// index is readable by anything that joins the network. Neither is what is
// wanted, which is "reachable from my machines".
//
// So the flag takes a LIST. `--addr 127.0.0.1:7420,10.4.0.3:7420` binds the
// loopback and the VPN address and nothing else — a box that later joins the
// LAN cannot reach it, without anyone having to remember to re-narrow a flag.
//
// The shorthand exists because the useful entries are tedious to type and easy
// to get wrong: `.76` means "the address ending .76 on one of my own
// interfaces", resolved against what this host actually holds. It cannot invent
// an address — a suffix matching no interface is an error naming what was
// available, rather than a bind to something unintended.

// parseListenList expands a comma-separated address list into bind addresses.
//
// Each entry is host:port, a bare host (taking the default port), or a `.N`
// suffix matched against this host's own IPv4 addresses.
func parseListenList(spec, defPort string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, raw := range strings.Split(spec, ",") {
		e := strings.TrimSpace(raw)
		if e == "" {
			continue
		}
		host, port := e, defPort
		if h, p, err := net.SplitHostPort(e); err == nil {
			host, port = h, p
		}
		hosts, err := resolveListenHost(host)
		if err != nil {
			return nil, err
		}
		for _, h := range hosts {
			a := net.JoinHostPort(h, port)
			if !seen[a] {
				seen[a] = true
				out = append(out, a)
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no listen address in %q", spec)
	}
	return out, nil
}

// resolveListenHost turns one entry into concrete hosts.
//
// A `.N` suffix is matched against this machine's own interface addresses. The
// match is on the final octet so `.76` finds 192.168.1.76 without anyone typing
// the prefix, and an ambiguous suffix returns every match rather than picking
// one — binding a guess is worse than being told there are two.
func resolveListenHost(host string) ([]string, error) {
	if !strings.HasPrefix(host, ".") {
		return []string{host}, nil
	}
	want := strings.TrimPrefix(host, ".")
	own, err := ownIPv4()
	if err != nil {
		return nil, err
	}
	var hit []string
	for _, ip := range own {
		if octets := strings.Split(ip, "."); len(octets) == 4 && octets[3] == want {
			hit = append(hit, ip)
		}
	}
	if len(hit) == 0 {
		return nil, fmt.Errorf("no interface address ends .%s (this host has %s)", want, strings.Join(own, ", "))
	}
	return hit, nil
}

// ownIPv4 lists this host's global IPv4 addresses.
func ownIPv4() ([]string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := n.IP.To4()
		if ip == nil || ip.IsLoopback() {
			continue
		}
		out = append(out, ip.String())
	}
	return out, nil
}
