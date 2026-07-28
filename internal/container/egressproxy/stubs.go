package main

import "net"

type certAuthority struct{}

func loadOrCreateCA(dir string) (*certAuthority, error)                                    { return &certAuthority{}, nil }
func matchRule(rules []MaskRule, host string) *MaskRule                                    { return nil }
func terminate(c net.Conn, sni string, rule *MaskRule, dialPort string, ca *certAuthority) { c.Close() }
