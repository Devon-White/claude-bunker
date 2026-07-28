package main

import "net"

func matchRule(rules []MaskRule, host string) *MaskRule                                    { return nil }
func terminate(c net.Conn, sni string, rule *MaskRule, dialPort string, ca *certAuthority) { c.Close() }
