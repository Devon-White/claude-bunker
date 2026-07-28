package main

import (
	"flag"
	"log"
	"net"
)

func main() {
	var cfg Config
	flag.StringVar(&cfg.ListenAddr, "listen", "127.0.0.1:15443", "listen address")
	flag.StringVar(&cfg.AllowlistPath, "allowlist", "", "path to the SNI allowlist (domains file)")
	flag.StringVar(&cfg.MaskingPath, "masking", "", "path to the masking config JSON (optional; Tier 2)")
	flag.StringVar(&cfg.CADir, "ca-dir", "", "directory for the per-container CA (Tier 2)")
	flag.Parse()
	if err := run(cfg); err != nil {
		log.Fatalf("egress-proxy: %v", err)
	}
}

func run(cfg Config) error {
	al, err := LoadAllowlist(cfg.AllowlistPath)
	if err != nil {
		return err
	}
	rules, err := LoadMasking(cfg.MaskingPath)
	if err != nil {
		return err
	}
	var ca *certAuthority
	if len(rules) > 0 {
		if ca, err = loadOrCreateCA(cfg.CADir); err != nil {
			return err
		}
	}
	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return err
	}
	serveWithCA(ln, cfg, al, rules, "443", ca)
	return nil
}

// serve is the Tier-1 entry point (no CA); serveWithCA adds termination.
func serve(ln net.Listener, cfg Config, al *Allowlist, rules []MaskRule, dialPort string) {
	serveWithCA(ln, cfg, al, rules, dialPort, nil)
}

func serveWithCA(ln net.Listener, cfg Config, al *Allowlist, rules []MaskRule, dialPort string, ca *certAuthority) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go handle(conn, al, rules, dialPort, ca)
	}
}

func handle(conn net.Conn, al *Allowlist, rules []MaskRule, dialPort string, ca *certAuthority) {
	sni, raw, err := readClientHello(conn)
	if err != nil || sni == "" || !al.Allowed(sni) {
		conn.Close() // fail closed
		return
	}
	if ms := matchRules(rules, sni); len(ms) > 0 && ca != nil {
		// readClientHello above already consumed the ClientHello record off
		// the socket; replay it so terminate's TLS handshake reader sees it.
		terminate(&prefixConn{Conn: conn, prefix: raw}, sni, ms, dialPort, ca)
		return
	}
	splice(conn, sni, string(raw), dialPort)
}
