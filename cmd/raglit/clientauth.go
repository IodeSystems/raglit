package main

import (
	"net/http"
	"net/url"
	"sync"

	"github.com/iodesystems/raglit"
	"github.com/iodesystems/raglit/client"
)

// The CLI's side of daemon auth. Commands reach the daemon through
// http.DefaultClient / http.Get / http.Post from many places, so the token is
// added by a transport installed once, in main. It attaches the token ONLY to a
// host ensureDaemon resolved as the daemon: the same process also calls model
// endpoints, and a daemon token sent there is a leaked credential.

var daemonHosts sync.Map // host → true

// trustDaemon marks base's host as the daemon, so requests to it carry the token.
func trustDaemon(base string) {
	if u, err := url.Parse(base); err == nil && u.Host != "" {
		daemonHosts.Store(u.Host, true)
	}
}

// installDaemonAuth wraps http.DefaultTransport. The daemon is never open, so
// there is always a source: $RAGLIT_TOKEN, a token command, else the admin
// token file of a daemon on this machine.
func installDaemonAuth() {
	cmd := ""
	if cfg, ok, err := raglit.LoadConfig(raglit.DiscoverHome()); err == nil && ok && cfg.Auth != nil {
		cmd = cfg.Auth.TokenCommand
	}
	src := client.EnvTokenSource(cmd)
	if src == nil {
		src = client.LocalAdminTokenSource()
	}
	http.DefaultTransport = &client.Transport{
		Base: http.DefaultTransport,
		Src:  src,
		Allow: func(host string) bool {
			_, ok := daemonHosts.Load(host)
			return ok
		},
	}
}
