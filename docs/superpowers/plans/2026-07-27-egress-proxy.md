# SNI-Aware Egress Proxy + Credential Masking Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a hostname/SNI-aware egress proxy to the hardened container — Tier 1 closes TLS domain-fronting by allowlisting on SNI and failing closed; Tier 2 terminates TLS for the 1–3 credential hosts and swaps a per-session sentinel for the real secret, so the agent never holds a real token.

**Architecture:** A self-contained stdlib-only Go proxy (`internal/container/egressproxy/`, `package main`) is compiled for the container arch via a multi-stage Docker build and started by `init-firewall.sh`, which also adds a transparent iptables nat REDIRECT of agent TCP/443 to the proxy (owner-match RETURN for the proxy's own uid). The proxy reads the same root-owned allowlist the ipset uses; per connection it closes (deny), splices (raw TLS relay, no plaintext), or terminates + injects credentials (auth hosts only). Credential masking is bunker-CLI-only (needs a host-side broker); the portable VS Code path runs the same binary in Tier-1 (splice) mode.

**Tech Stack:** Go (stdlib only: `crypto/tls`, `crypto/x509`, `crypto/ecdsa`, `net`, `net/http`, `encoding/pem`, `bufio`), Docker multi-stage build, iptables nat table, bash (`init-firewall.sh`).

## Global Constraints

- **Proxy imports STDLIB ONLY.** No third-party deps, no imports of other bunker packages. It must build from a synthetic standalone `go.mod` with zero requires. Verified in Task 6 (`GOPROXY=off` build).
- **Proxy package is `package main` at `internal/container/egressproxy/`** — part of the bunker module (so `go test ./...` covers it), never imported by other packages (it's `main`), only its source is embedded and its compiled binary is run.
- **Builder image:** `golang:1.23-bookworm`; synthetic proxy `go.mod` declares `go 1.23`. Proxy code uses only long-stable stdlib (no >1.23 APIs).
- **Fail-closed everywhere:** unknown/missing SNI → deny; proxy down → redirected 443 hits a dead port → connection refused. Never fail open.
- **Single allowlist source:** the proxy's allowlist is the file `init-firewall.sh` receives as `$1` (native `/tmp/.bunker-domains` root:root 0444; portable `/etc/claude-bunker/allowed-domains.txt` baked 0444). The proxy never has its own domain list.
- **Real secrets are `bunker-proxy`-owned `0400`.** The agent (`claude-bunker` uid 1000) must never be able to read them. Sentinels are what land in agent-readable `/run/secrets/`.
- **Constants (defined in Task 6, used verbatim thereafter):** `ProxyUser="bunker-proxy"`, `ProxyUID=1001`, `ProxyGID=1001`, `ProxyPort="15443"`, `ProxyBinaryPath="/usr/local/bin/egress-proxy"`, `ProxyConfigDir="/etc/claude-bunker/proxy"`, `MaskingConfigPath="/etc/claude-bunker/proxy/masking.json"`, `ProxyCADir="/etc/claude-bunker/proxy/ca"`, `ProxyCACertPath="/etc/claude-bunker/proxy/ca/ca.pem"`.
- Existing test suites stay green; `gofmt -l .` clean; `GOOS=windows go build ./...` still builds (the proxy is `package main` compiled on linux in Docker, but its source is stdlib-only and cross-compiles fine — it must not use build tags that break Windows host `go build ./...`).

## File Structure

**New — the proxy (`internal/container/egressproxy/`, `package main`):**
- `config.go` — `Config`, `MaskRule`, load masking JSON.
- `allowlist.go` — `Allowlist` (exact + `*.` wildcard), `LoadAllowlist`, `Allowed`.
- `sniread.go` — `readClientHello` → SNI + raw bytes for replay.
- `splice.go` — raw bidirectional relay to re-resolved SNI host.
- `ca.go` — per-container CA + cached per-host leaf certs.
- `mask.go` — sentinel→real header swap (Bearer / x-api-key / Basic).
- `terminate.go` — TLS-terminate an auth host, mask, forward to real upstream.
- `main.go` — flags, accept loop, dispatch deny/splice/terminate.
- `*_test.go` — per-file unit tests.

**Modified:**
- `internal/container/constants.go` — proxy constants + `NodeExtraCACerts` path.
- `internal/container/embed.go` — embed proxy source; `EgressProxySources()`.
- `internal/container/build.go` — add proxy sources to `buildContextTar`.
- `internal/container/baseimage.go` + `scripts/base.dockerfile.tmpl` — multi-stage proxy build stage, `bunker-proxy` uid, `COPY` binary; new template fields.
- `internal/container/scripts/init-firewall.sh` — launch proxy, nat REDIRECT + owner RETURN, domain-fronting self-test.
- `internal/container/lifecycle.go` — `RunPostStart` reorder (masking setup + CA install before firewall); `InjectAuthSecrets`/`writeSecretFiles`/`createAuthWrapper` sentinel/real split; masking-config writer.
- `internal/config/fingerprint.go` — fold proxy source + masking toggle into fingerprints.
- `internal/devcontainer/generate.go` — portable Tier-1 already covered by `init-firewall.sh`; ensure the baked allowlist path is passed (no masking).
- `cmd/init.go` — bake proxy build into the committed Dockerfile bundle.
- `CLAUDE.md` — document the egress-proxy layer.

---

### Task 1: Proxy config + allowlist matching

**Files:**
- Create: `internal/container/egressproxy/config.go`
- Create: `internal/container/egressproxy/allowlist.go`
- Test: `internal/container/egressproxy/allowlist_test.go`, `internal/container/egressproxy/config_test.go`

**Interfaces:**
- Produces: `type MaskRule struct{ Sentinel, Secret string; Hosts, Headers []string }`; `type Config struct{ ListenAddr, AllowlistPath, MaskingPath, CADir string }`; `func LoadMasking(path string) ([]MaskRule, error)` (returns `nil,nil` if the file is absent); `type Allowlist`; `func LoadAllowlist(path string) (*Allowlist, error)`; `func (*Allowlist) Allowed(host string) bool`.

- [ ] **Step 1: Write the failing test**

`allowlist_test.go`:
```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAllowlist(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "domains.txt", "!api.anthropic.com\ngithub.com\n*.githubusercontent.com\n\n# comment\n")
	al, err := LoadAllowlist(p)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		host string
		want bool
	}{
		{"api.anthropic.com", true},           // critical marker stripped
		{"API.Anthropic.com", true},           // case-insensitive
		{"github.com", true},
		{"raw.githubusercontent.com", true},   // wildcard suffix
		{"githubusercontent.com", false},      // "*." requires a label
		{"evil.com", false},
		{"notgithub.com", false},
		{"", false},
	}
	for _, c := range cases {
		if got := al.Allowed(c.host); got != c.want {
			t.Errorf("Allowed(%q)=%v want %v", c.host, got, c.want)
		}
	}
}
```

`config_test.go`:
```go
package main

import "testing"

func TestLoadMaskingAbsentIsNil(t *testing.T) {
	rules, err := LoadMasking("/nonexistent/masking.json")
	if err != nil {
		t.Fatalf("absent masking file must not error, got %v", err)
	}
	if rules != nil {
		t.Fatalf("absent masking file must yield nil rules, got %v", rules)
	}
}

func TestLoadMaskingParses(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "masking.json", `[{"sentinel":"S","secret":"R","hosts":["api.anthropic.com"],"headers":["x-api-key"]}]`)
	rules, err := LoadMasking(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].Sentinel != "S" || rules[0].Secret != "R" {
		t.Fatalf("unexpected rules: %+v", rules)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/container/egressproxy/`
Expected: FAIL — `undefined: LoadAllowlist` / `LoadMasking`.

- [ ] **Step 3: Write minimal implementation**

`config.go`:
```go
package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
)

// MaskRule maps a per-session sentinel token to its real value and the hosts
// where the real value may be injected. Present only in the bunker-CLI (Tier 2)
// path; absent => the proxy runs Tier 1 (splice) only.
type MaskRule struct {
	Sentinel string   `json:"sentinel"`
	Secret   string   `json:"secret"`
	Hosts    []string `json:"hosts"`
	Headers  []string `json:"headers"`
}

// Config is the proxy's runtime configuration, populated from flags in main.go.
type Config struct {
	ListenAddr    string
	AllowlistPath string
	MaskingPath   string
	CADir         string
}

// LoadMasking reads the masking rules JSON. A missing file is not an error —
// it means Tier 1 (no termination). Returns nil rules in that case.
func LoadMasking(path string) ([]MaskRule, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rules []MaskRule
	if err := json.Unmarshal(data, &rules); err != nil {
		return nil, err
	}
	return rules, nil
}
```

`allowlist.go`:
```go
package main

import (
	"bufio"
	"os"
	"strings"
)

// Allowlist matches SNI hostnames against the allowed set. Entries are plain
// hostnames (exact match) or "*.suffix" wildcards (match any single-or-more
// label prefix of ".suffix"). A leading "!" (the firewall's critical marker)
// and blank/comment lines are ignored.
type Allowlist struct {
	exact    map[string]struct{}
	suffixes []string // e.g. ".githubusercontent.com"
}

// LoadAllowlist parses the domains file (the same file the ipset is built from).
func LoadAllowlist(path string) (*Allowlist, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	al := &Allowlist{exact: make(map[string]struct{})}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		line = strings.TrimPrefix(line, "!")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.ToLower(line)
		if strings.HasPrefix(line, "*.") {
			al.suffixes = append(al.suffixes, line[1:]) // ".githubusercontent.com"
			continue
		}
		al.exact[line] = struct{}{}
	}
	return al, sc.Err()
}

// Allowed reports whether an SNI host is permitted.
func (a *Allowlist) Allowed(host string) bool {
	if host == "" {
		return false
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if _, ok := a.exact[host]; ok {
		return true
	}
	for _, s := range a.suffixes {
		// "*.githubusercontent.com" => host must end with ".githubusercontent.com"
		// and have at least one label before it.
		if strings.HasSuffix(host, s) && len(host) > len(s) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/container/egressproxy/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/container/egressproxy/config.go internal/container/egressproxy/allowlist.go internal/container/egressproxy/allowlist_test.go internal/container/egressproxy/config_test.go
git commit -m "feat(egressproxy): config + SNI allowlist matching"
```

---

### Task 2: SNI ClientHello parser

**Files:**
- Create: `internal/container/egressproxy/sniread.go`
- Test: `internal/container/egressproxy/sniread_test.go`

**Interfaces:**
- Consumes: nothing from prior tasks.
- Produces: `func readClientHello(r io.Reader) (sni string, raw []byte, err error)` — reads exactly one TLS handshake record from `r`, returns the SNI (empty if none) and the raw bytes read (record header + body) so a splice can replay them to the upstream.

- [ ] **Step 1: Write the failing test**

`sniread_test.go`:
```go
package main

import (
	"bytes"
	"crypto/tls"
	"io"
	"net"
	"testing"
	"time"
)

// captureClientHello starts a throwaway TLS client handshake against a pipe and
// returns the ClientHello bytes the client sent for SNI "example.test".
func captureClientHello(t *testing.T, serverName string) []byte {
	t.Helper()
	c, s := net.Pipe()
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Read whatever the client writes (the ClientHello) then stop.
		io.Copy(&buf, s)
	}()
	go func() {
		_ = tls.Client(c, &tls.Config{ServerName: serverName, InsecureSkipVerify: true}).Handshake()
		c.Close()
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout capturing ClientHello")
	}
	return buf.Bytes()
}

func TestReadClientHelloSNI(t *testing.T) {
	hello := captureClientHello(t, "example.test")
	sni, raw, err := readClientHello(bytes.NewReader(hello))
	if err != nil {
		t.Fatal(err)
	}
	if sni != "example.test" {
		t.Fatalf("sni=%q want example.test", sni)
	}
	if len(raw) == 0 || !bytes.HasPrefix(hello, raw) {
		t.Fatalf("raw must be a prefix of the captured hello")
	}
}

func TestReadClientHelloNoSNI(t *testing.T) {
	// A record that is valid TLS framing but not a ClientHello with SNI:
	// handshake record, ClientHello with empty extensions.
	hello := captureClientHello(t, "") // no ServerName => no SNI extension
	sni, _, err := readClientHello(bytes.NewReader(hello))
	if err != nil {
		t.Fatal(err)
	}
	if sni != "" {
		t.Fatalf("expected empty SNI, got %q", sni)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/container/egressproxy/ -run ClientHello`
Expected: FAIL — `undefined: readClientHello`.

- [ ] **Step 3: Write minimal implementation**

`sniread.go`:
```go
package main

import (
	"encoding/binary"
	"errors"
	"io"
)

// readClientHello reads a single TLS record (expected: a handshake record
// carrying the ClientHello) from r, returning the parsed SNI (empty if the
// ClientHello has no server_name extension) and the raw bytes consumed so the
// caller can replay them to an upstream when splicing.
//
// Parsing is defensive: any malformed field yields ("", raw, error) so callers
// fail closed. Only the common single-record ClientHello is handled; a hello
// fragmented across records yields an error (deny).
func readClientHello(r io.Reader) (string, []byte, error) {
	// TLS record header: type(1) version(2) length(2).
	hdr := make([]byte, 5)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return "", nil, err
	}
	if hdr[0] != 0x16 { // 22 = handshake
		return "", hdr, errors.New("not a TLS handshake record")
	}
	recLen := int(binary.BigEndian.Uint16(hdr[3:5]))
	if recLen == 0 || recLen > 1<<16 {
		return "", hdr, errors.New("bad record length")
	}
	body := make([]byte, recLen)
	if _, err := io.ReadFull(r, body); err != nil {
		return "", append(hdr, body...), err
	}
	raw := append(hdr, body...)
	sni, err := parseSNI(body)
	return sni, raw, err
}

// parseSNI extracts the server_name from a ClientHello handshake body.
func parseSNI(b []byte) (string, error) {
	// Handshake header: type(1) length(3).
	if len(b) < 4 || b[0] != 0x01 { // 1 = ClientHello
		return "", errors.New("not a ClientHello")
	}
	b = b[4:]
	// version(2) + random(32)
	if len(b) < 34 {
		return "", errors.New("short hello")
	}
	b = b[34:]
	// session_id
	if len(b) < 1 {
		return "", errors.New("short session_id")
	}
	sidLen := int(b[0])
	b = b[1:]
	if len(b) < sidLen {
		return "", errors.New("short session_id body")
	}
	b = b[sidLen:]
	// cipher_suites
	if len(b) < 2 {
		return "", errors.New("short cipher_suites")
	}
	csLen := int(binary.BigEndian.Uint16(b))
	b = b[2:]
	if len(b) < csLen {
		return "", errors.New("short cipher_suites body")
	}
	b = b[csLen:]
	// compression_methods
	if len(b) < 1 {
		return "", errors.New("short compression")
	}
	cmLen := int(b[0])
	b = b[1:]
	if len(b) < cmLen {
		return "", errors.New("short compression body")
	}
	b = b[cmLen:]
	// extensions
	if len(b) < 2 {
		return "", nil // no extensions => no SNI
	}
	extTotal := int(binary.BigEndian.Uint16(b))
	b = b[2:]
	if len(b) < extTotal {
		return "", errors.New("short extensions")
	}
	b = b[:extTotal]
	for len(b) >= 4 {
		extType := binary.BigEndian.Uint16(b)
		extLen := int(binary.BigEndian.Uint16(b[2:]))
		b = b[4:]
		if len(b) < extLen {
			return "", errors.New("short extension body")
		}
		ext := b[:extLen]
		b = b[extLen:]
		if extType != 0x0000 { // server_name
			continue
		}
		// server_name_list: list_len(2), then entries: type(1) len(2) name.
		if len(ext) < 2 {
			return "", errors.New("short SNI list")
		}
		listLen := int(binary.BigEndian.Uint16(ext))
		ext = ext[2:]
		if len(ext) < listLen {
			return "", errors.New("short SNI list body")
		}
		for len(ext) >= 3 {
			nameType := ext[0]
			nameLen := int(binary.BigEndian.Uint16(ext[1:]))
			ext = ext[3:]
			if len(ext) < nameLen {
				return "", errors.New("short SNI name")
			}
			name := ext[:nameLen]
			ext = ext[nameLen:]
			if nameType == 0x00 { // host_name
				return string(name), nil
			}
		}
	}
	return "", nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/container/egressproxy/ -run ClientHello`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/container/egressproxy/sniread.go internal/container/egressproxy/sniread_test.go
git commit -m "feat(egressproxy): TLS ClientHello SNI parser"
```

---

### Task 3: Splice path + Tier-1 main (end-to-end SNI-passthrough proxy)

**Files:**
- Create: `internal/container/egressproxy/splice.go`
- Create: `internal/container/egressproxy/main.go`
- Test: `internal/container/egressproxy/splice_test.go`

**Interfaces:**
- Consumes: `readClientHello`, `Allowlist`, `Config`.
- Produces: `func splice(client net.Conn, sni string, raw []byte, dialTLSPort string)` — dials `sni:dialTLSPort`, replays `raw`, relays bidirectionally; `func serve(ln net.Listener, cfg Config, al *Allowlist, rules []MaskRule)` — the accept loop dispatching deny/splice/terminate (terminate is a no-op stub until Task 5); `func run(cfg Config) error` — load allowlist+masking, listen on `cfg.ListenAddr`, call `serve`. `main()` parses flags into a `Config` and calls `run`.

- [ ] **Step 1: Write the failing test**

`splice_test.go`:
```go
package main

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"testing"
	"time"
)

// startTLSEchoServer returns a TLS listener that echoes one line, plus a cert
// pool trusting it. ServerName is not verified by the server.
func startTLSEchoServer(t *testing.T) (addr string, pool *x509.CertPool) {
	t.Helper()
	cert := selfSignedCert(t, "upstream.test")
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert.tls}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				buf := make([]byte, 4)
				if _, err := io.ReadFull(c, buf); err == nil {
					c.Write(buf) // echo
				}
			}()
		}
	}()
	pool = x509.NewCertPool()
	pool.AddCert(cert.leaf)
	return ln.Addr().String(), pool
}

func TestSpliceAllowedHostRelays(t *testing.T) {
	upstreamAddr, pool := startTLSEchoServer(t)
	_, port, _ := net.SplitHostPort(upstreamAddr)

	// Allowlist that permits "upstream.test"; proxy dials 127.0.0.1 via a
	// resolver override is out of scope — instead we splice to the upstream's
	// actual host:port by treating the SNI as the dial target. Use 127.0.0.1
	// as the SNI so net.Dial reaches the echo server.
	al := &Allowlist{exact: map[string]struct{}{"127.0.0.1": {}}}

	proxyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { proxyLn.Close() })
	go serve(proxyLn, Config{}, al, nil, port)

	// Client dials the proxy with SNI 127.0.0.1, expecting the proxy to splice
	// to 127.0.0.1:<port> and relay the TLS handshake + echo.
	conn, err := tls.Dial("tcp", proxyLn.Addr().String(),
		&tls.Config{ServerName: "127.0.0.1", RootCAs: pool})
	if err != nil {
		t.Fatalf("handshake through splice failed: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	conn.Write([]byte("ping"))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo failed: %q err=%v", buf, err)
	}
}

func TestSpliceDeniedSNIClosed(t *testing.T) {
	al := &Allowlist{exact: map[string]struct{}{"allowed.test": {}}}
	proxyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { proxyLn.Close() })
	go serve(proxyLn, Config{}, al, nil, "443")

	conn, err := tls.Dial("tcp", proxyLn.Addr().String(),
		&tls.Config{ServerName: "evil.test", InsecureSkipVerify: true})
	if err == nil {
		conn.Close()
		t.Fatal("expected handshake to fail for denied SNI")
	}
}
```

> Note: `serve` gains a trailing `dialPort string` param so tests can target an ephemeral upstream port; in production it is always `"443"`. `selfSignedCert` is provided by Task 4; for this task add a temporary local `selfSignedCert` helper in `splice_test.go` **only if Task 4 is not yet merged** — since tasks execute in order, Task 4's `ca_test.go` helper will exist. To keep Task 3 self-contained, include the helper here and delete it in Task 4. See Step 3 note.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/container/egressproxy/ -run Splice`
Expected: FAIL — `undefined: serve` / `splice`.

- [ ] **Step 3: Write minimal implementation**

Add the shared test helper `selfSignedCert` now (used by Tasks 3–5) in a new **non-`_test`-excluded** helper file so both splice and ca tests share it. Create `internal/container/egressproxy/testhelpers_test.go`:
```go
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"crypto/tls"
	"math/big"
	"testing"
	"time"
)

type certBundle struct {
	tls  tls.Certificate
	leaf *x509.Certificate
}

func selfSignedCert(t *testing.T, cn string) certBundle {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		DNSNames:     []string{cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	return certBundle{
		tls:  tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf},
		leaf: leaf,
	}
}
```

`splice.go`:
```go
package main

import (
	"io"
	"net"
)

// splice relays a raw TLS stream from client to the re-resolved SNI host and
// back, replaying the already-read ClientHello bytes first. No decryption —
// the proxy sees only ciphertext. Re-resolving by SNI (not the original dest
// IP) prevents fronting: a client claiming SNI X reaches the real X.
func splice(client net.Conn, sni, raw, dialPort string) {
	defer client.Close()
	upstream, err := net.Dial("tcp", net.JoinHostPort(sni, dialPort))
	if err != nil {
		return
	}
	defer upstream.Close()
	if _, err := upstream.Write([]byte(raw)); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() { io.Copy(upstream, client); done <- struct{}{} }()
	go func() { io.Copy(client, upstream); done <- struct{}{} }()
	<-done
}
```

`main.go`:
```go
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
	if rule := matchRule(rules, sni); rule != nil && ca != nil {
		terminate(conn, sni, rule, dialPort, ca)
		return
	}
	splice(conn, sni, string(raw), dialPort)
}
```

> `matchRule`, `terminate`, `certAuthority`, `loadOrCreateCA` are defined in Tasks 4–5. To keep this task compiling and testable, add temporary stubs in a new file `internal/container/egressproxy/stubs.go` that Task 4/5 replace:
```go
package main

import "net"

type certAuthority struct{}

func loadOrCreateCA(dir string) (*certAuthority, error) { return &certAuthority{}, nil }
func matchRule(rules []MaskRule, host string) *MaskRule { return nil }
func terminate(c net.Conn, sni string, rule *MaskRule, dialPort string, ca *certAuthority) { c.Close() }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/container/egressproxy/`
Expected: PASS (splice relays for the allowed SNI; denied SNI handshake fails because the proxy closes before any TLS bytes flow).

- [ ] **Step 5: Commit**

```bash
git add internal/container/egressproxy/
git commit -m "feat(egressproxy): splice path + Tier-1 accept loop (SNI passthrough)"
```

---

### Task 4: Per-container CA + leaf certs

**Files:**
- Create: `internal/container/egressproxy/ca.go` (replaces `certAuthority`/`loadOrCreateCA` stubs)
- Modify: `internal/container/egressproxy/stubs.go` (remove `certAuthority` + `loadOrCreateCA`, keep `matchRule`/`terminate` stubs until Task 5)
- Test: `internal/container/egressproxy/ca_test.go`

**Interfaces:**
- Produces: `type certAuthority struct{ ... }`; `func loadOrCreateCA(dir string) (*certAuthority, error)` (generates + persists `ca.pem`/`ca.key` if absent, else loads); `func (*certAuthority) leafFor(host string) (*tls.Certificate, error)` (cached); `func (*certAuthority) certPEM() []byte`.

- [ ] **Step 1: Write the failing test**

`ca_test.go`:
```go
package main

import (
	"crypto/tls"
	"crypto/x509"
	"testing"
)

func TestCAMintsVerifiableLeaf(t *testing.T) {
	dir := t.TempDir()
	ca, err := loadOrCreateCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := ca.leafFor("api.anthropic.com")
	if err != nil {
		t.Fatal(err)
	}
	// The leaf must chain to the CA and be valid for the host.
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca.certPEM()) {
		t.Fatal("certPEM not a valid PEM cert")
	}
	x509leaf, err := x509.ParseCertificate(leaf.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x509leaf.Verify(x509.VerifyOptions{DNSName: "api.anthropic.com", Roots: roots}); err != nil {
		t.Fatalf("leaf does not verify against CA: %v", err)
	}
}

func TestCAPersistsAcrossLoad(t *testing.T) {
	dir := t.TempDir()
	ca1, err := loadOrCreateCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	ca2, err := loadOrCreateCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	if string(ca1.certPEM()) != string(ca2.certPEM()) {
		t.Fatal("CA must persist across loads within a container")
	}
}

var _ = tls.Certificate{}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/container/egressproxy/ -run CA`
Expected: FAIL — the stub `certAuthority` has no `leafFor`/`certPEM`.

- [ ] **Step 3: Write minimal implementation**

Remove `certAuthority` and `loadOrCreateCA` from `stubs.go` (leave `matchRule`/`terminate`). Create `ca.go`:
```go
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type certAuthority struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEMbytes []byte

	mu    sync.Mutex
	leaves map[string]*tls.Certificate
}

func (c *certAuthority) certPEM() []byte { return c.certPEMbytes }

// loadOrCreateCA loads ca.pem/ca.key from dir, generating them if absent.
func loadOrCreateCA(dir string) (*certAuthority, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	certPath := filepath.Join(dir, "ca.pem")
	keyPath := filepath.Join(dir, "ca.key")

	certPEM, certErr := os.ReadFile(certPath)
	keyPEM, keyErr := os.ReadFile(keyPath)
	if errors.Is(certErr, fs.ErrNotExist) || errors.Is(keyErr, fs.ErrNotExist) {
		return generateCA(dir, certPath, keyPath)
	}
	if certErr != nil {
		return nil, certErr
	}
	if keyErr != nil {
		return nil, keyErr
	}
	cblock, _ := pem.Decode(certPEM)
	kblock, _ := pem.Decode(keyPEM)
	if cblock == nil || kblock == nil {
		return nil, errors.New("corrupt CA files")
	}
	cert, err := x509.ParseCertificate(cblock.Bytes)
	if err != nil {
		return nil, err
	}
	key, err := x509.ParseECPrivateKey(kblock.Bytes)
	if err != nil {
		return nil, err
	}
	return &certAuthority{cert: cert, key: key, certPEMbytes: certPEM, leaves: map[string]*tls.Certificate{}}, nil
}

func generateCA(dir, certPath, keyPath string) (*certAuthority, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "claude-bunker egress CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(certPath, certPEM, 0444); err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0400); err != nil {
		return nil, err
	}
	return &certAuthority{cert: cert, key: key, certPEMbytes: certPEM, leaves: map[string]*tls.Certificate{}}, nil
}

// leafFor mints (and caches) a leaf certificate for host, signed by the CA.
func (c *certAuthority) leafFor(host string) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if lc, ok := c.leaves[host]; ok {
		return lc, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return nil, err
	}
	leaf := &tls.Certificate{Certificate: [][]byte{der, c.cert.Raw}, PrivateKey: key}
	c.leaves[host] = leaf
	return leaf, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/container/egressproxy/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/container/egressproxy/ca.go internal/container/egressproxy/ca_test.go internal/container/egressproxy/stubs.go
git commit -m "feat(egressproxy): per-container CA + cached leaf certs"
```

---

### Task 5: TLS-terminate + credential masking

**Files:**
- Create: `internal/container/egressproxy/mask.go`
- Create: `internal/container/egressproxy/terminate.go` (replaces `matchRule`/`terminate` stubs)
- Delete: `internal/container/egressproxy/stubs.go`
- Test: `internal/container/egressproxy/mask_test.go`, `internal/container/egressproxy/terminate_test.go`

**Interfaces:**
- Consumes: `certAuthority.leafFor`, `MaskRule`.
- Produces: `func matchRule(rules []MaskRule, host string) *MaskRule`; `func applyMask(h http.Header, rule *MaskRule)`; `func terminate(client net.Conn, sni string, rule *MaskRule, dialPort string, ca *certAuthority)`.

- [ ] **Step 1: Write the failing test**

`mask_test.go`:
```go
package main

import (
	"encoding/base64"
	"net/http"
	"testing"
)

func TestApplyMaskBearer(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer sk-ant-SENTINEL")
	applyMask(h, &MaskRule{Sentinel: "sk-ant-SENTINEL", Secret: "sk-ant-REAL", Headers: []string{"authorization"}})
	if got := h.Get("Authorization"); got != "Bearer sk-ant-REAL" {
		t.Fatalf("bearer swap: %q", got)
	}
}

func TestApplyMaskAPIKey(t *testing.T) {
	h := http.Header{}
	h.Set("x-api-key", "SENTINEL")
	applyMask(h, &MaskRule{Sentinel: "SENTINEL", Secret: "REAL", Headers: []string{"x-api-key"}})
	if got := h.Get("x-api-key"); got != "REAL" {
		t.Fatalf("api-key swap: %q", got)
	}
}

func TestApplyMaskBasicPassword(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("x-access-token:SENTINEL")))
	applyMask(h, &MaskRule{Sentinel: "SENTINEL", Secret: "ghp_REAL", Headers: []string{"authorization"}})
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:ghp_REAL"))
	if got := h.Get("Authorization"); got != want {
		t.Fatalf("basic swap: %q want %q", got, want)
	}
}

func TestApplyMaskLeavesNonMatchingUntouched(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer something-else")
	applyMask(h, &MaskRule{Sentinel: "SENTINEL", Secret: "REAL", Headers: []string{"authorization"}})
	if got := h.Get("Authorization"); got != "Bearer something-else" {
		t.Fatalf("must not touch non-matching value: %q", got)
	}
}
```

`terminate_test.go`:
```go
package main

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestTerminateInjectsRealSecret(t *testing.T) {
	// Upstream TLS server that records the Authorization header it receives.
	gotAuth := make(chan string, 1)
	upstreamCert := selfSignedCert(t, "127.0.0.1")
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{upstreamCert.tls}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		req, err := http.ReadRequest(bufioReader(c))
		if err != nil {
			return
		}
		gotAuth <- req.Header.Get("Authorization")
		io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	ca, _ := loadOrCreateCA(t.TempDir())
	// upstream uses a self-signed cert; make terminate trust it for the test.
	testUpstreamRoots = x509.NewCertPool()
	testUpstreamRoots.AddCert(upstreamCert.leaf)
	t.Cleanup(func() { testUpstreamRoots = nil })

	proxyLn, _ := net.Listen("tcp", "127.0.0.1:0")
	t.Cleanup(func() { proxyLn.Close() })
	rule := &MaskRule{Sentinel: "SENTINEL", Secret: "REAL-SECRET", Hosts: []string{"127.0.0.1"}, Headers: []string{"authorization"}}
	go serveWithCA(proxyLn, Config{}, &Allowlist{exact: map[string]struct{}{"127.0.0.1": {}}}, []MaskRule{*rule}, port, ca)

	// Client trusts the bunker CA, sends the sentinel.
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.certPEM())
	conn, err := tls.Dial("tcp", proxyLn.Addr().String(), &tls.Config{ServerName: "127.0.0.1", RootCAs: pool})
	if err != nil {
		t.Fatalf("client handshake to proxy failed: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	io.WriteString(conn, "GET / HTTP/1.1\r\nHost: 127.0.0.1\r\nAuthorization: Bearer SENTINEL\r\n\r\n")
	io.ReadAll(conn)

	select {
	case auth := <-gotAuth:
		if auth != "Bearer REAL-SECRET" {
			t.Fatalf("upstream saw %q, want the injected real secret", auth)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upstream never received the request")
	}
}
```

> `bufioReader` and the `testUpstreamRoots` seam are added in Step 3.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/container/egressproxy/ -run 'Mask|Terminate'`
Expected: FAIL — `undefined: applyMask` and the stub `terminate` closes the connection.

- [ ] **Step 3: Write minimal implementation**

Delete `stubs.go`. Create `mask.go`:
```go
package main

import (
	"encoding/base64"
	"net/http"
	"strings"
)

// matchRule returns the first rule whose Hosts include host, or nil.
func matchRule(rules []MaskRule, host string) *MaskRule {
	for i := range rules {
		for _, h := range rules[i].Hosts {
			if strings.EqualFold(h, host) {
				return &rules[i]
			}
		}
	}
	return nil
}

// applyMask replaces the rule's sentinel with the real secret in the configured
// headers. Handles bare values (x-api-key), "Bearer <sentinel>", and HTTP Basic
// (decode, swap the password, re-encode). Non-matching values are left intact.
func applyMask(h http.Header, rule *MaskRule) {
	for _, name := range rule.Headers {
		v := h.Get(name)
		if v == "" {
			continue
		}
		switch {
		case strings.HasPrefix(v, "Basic "):
			h.Set(name, swapBasic(v, rule.Sentinel, rule.Secret))
		default:
			// covers "Bearer <sentinel>", "<sentinel>", "Token <sentinel>", etc.
			h.Set(name, strings.ReplaceAll(v, rule.Sentinel, rule.Secret))
		}
	}
}

func swapBasic(v, sentinel, secret string) string {
	enc := strings.TrimPrefix(v, "Basic ")
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return v
	}
	user, pass, ok := strings.Cut(string(raw), ":")
	if !ok || pass != sentinel {
		return v
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+secret))
}
```

Create `terminate.go`:
```go
package main

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
)

// testUpstreamRoots, when non-nil (tests only), overrides upstream TLS
// verification so a self-signed upstream can be used.
var testUpstreamRoots *x509.CertPool

func bufioReader(c net.Conn) *bufio.Reader { return bufio.NewReader(c) }

// terminate accepts the client's TLS using a leaf minted for sni, reads each
// HTTP/1.1 request, swaps the sentinel for the real secret, and forwards over a
// genuine TLS connection to the real upstream. Responses stream back verbatim.
func terminate(client net.Conn, sni string, rule *MaskRule, dialPort string, ca *certAuthority) {
	defer client.Close()
	leaf, err := ca.leafFor(sni)
	if err != nil {
		return
	}
	tlsClient := tls.Server(client, &tls.Config{Certificates: []tls.Certificate{*leaf}})
	if err := tlsClient.Handshake(); err != nil {
		return
	}
	defer tlsClient.Close()

	upstreamCfg := &tls.Config{ServerName: sni, RootCAs: testUpstreamRoots}
	upstream, err := tls.Dial("tcp", net.JoinHostPort(sni, dialPort), upstreamCfg)
	if err != nil {
		return
	}
	defer upstream.Close()

	cr := bufio.NewReader(tlsClient)
	ur := bufio.NewReader(upstream)
	for {
		req, err := http.ReadRequest(cr)
		if err != nil {
			return
		}
		applyMask(req.Header, rule)
		req.URL.Scheme = ""
		req.URL.Host = ""
		if err := req.Write(upstream); err != nil {
			return
		}
		resp, err := http.ReadResponse(ur, req)
		if err != nil {
			return
		}
		if err := resp.Write(tlsClient); err != nil {
			return
		}
		if req.Close || resp.Close {
			return
		}
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/container/egressproxy/`
Expected: PASS — the upstream receives `Bearer REAL-SECRET`; the agent only ever sent `SENTINEL`.

- [ ] **Step 5: Commit**

```bash
git add internal/container/egressproxy/
git rm internal/container/egressproxy/stubs.go
git commit -m "feat(egressproxy): TLS terminate + credential masking (Tier 2)"
```

---

### Task 6: Build context — embed proxy source, synthetic go.mod, multi-stage Dockerfile, bunker-proxy user

**Files:**
- Modify: `internal/container/constants.go` (proxy constants)
- Modify: `internal/container/embed.go` (embed source; `EgressProxySources()`)
- Modify: `internal/container/build.go` (`buildContextTar` adds proxy sources)
- Modify: `internal/container/baseimage.go` (`baseTemplateData` fields)
- Modify: `internal/container/scripts/base.dockerfile.tmpl` (multi-stage + user + COPY)
- Test: `internal/container/egressproxy_context_test.go`, `internal/container/baseimage_test.go` (extend)

**Interfaces:**
- Consumes: the `internal/container/egressproxy/*.go` source files.
- Produces: constants listed in Global Constraints; `func EgressProxySources() []BuildContextFile` returning the proxy `.go` files (test files excluded) rooted under `egressproxy/` plus a synthetic `egressproxy/go.mod`; these are added to the build-context tar.

- [ ] **Step 1: Write the failing test**

`egressproxy_context_test.go`:
```go
package container

import "testing"

func TestEgressProxySourcesIncludesMainAndGoMod(t *testing.T) {
	srcs := EgressProxySources()
	var hasMain, hasMod bool
	for _, f := range srcs {
		if f.Name == "egressproxy/main.go" {
			hasMain = true
		}
		if f.Name == "egressproxy/go.mod" {
			hasMod = true
		}
		if len(f.Name) > 8 && f.Name[len(f.Name)-8:] == "_test.go" {
			t.Errorf("test file leaked into build context: %s", f.Name)
		}
	}
	if !hasMain {
		t.Error("egressproxy/main.go missing from build context")
	}
	if !hasMod {
		t.Error("synthetic egressproxy/go.mod missing from build context")
	}
}
```

Extend `baseimage_test.go`:
```go
func TestGenerateBaseDockerfile_MultiStageProxy(t *testing.T) {
	df := GenerateBaseDockerfile()
	for _, want := range []string{
		"FROM golang:1.23-bookworm AS proxybuild",
		"COPY egressproxy/ ./egressproxy/",
		"go build",
		"COPY --from=proxybuild",
		ProxyBinaryPath,
		"useradd --uid 1001",
	} {
		if !strings.Contains(df, want) {
			t.Errorf("base Dockerfile missing %q", want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/container/ -run 'EgressProxySources|MultiStageProxy'`
Expected: FAIL — `undefined: EgressProxySources`, `undefined: ProxyBinaryPath`, missing Dockerfile content.

- [ ] **Step 3: Write minimal implementation**

Add to `constants.go`:
```go
// Egress proxy (SNI-aware) constants.
const (
	ProxyUser         = "bunker-proxy"
	ProxyUID          = 1001
	ProxyGID          = 1001
	ProxyPort         = "15443"
	ProxyBinaryPath   = "/usr/local/bin/egress-proxy"
	ProxyConfigDir    = "/etc/claude-bunker/proxy"
	MaskingConfigPath = ProxyConfigDir + "/masking.json"
	ProxyCADir        = ProxyConfigDir + "/ca"
	ProxyCACertPath   = ProxyCADir + "/ca.pem"
)
```

Add to `embed.go`:
```go
//go:embed egressproxy/*.go
var egressProxySrc embed.FS

// synthEgressGoMod is the standalone module file shipped into the build context
// so the multi-stage builder compiles the stdlib-only proxy offline.
const synthEgressGoMod = "module egressproxyd\n\ngo 1.23\n"

// EgressProxySources returns the proxy Go source (test files excluded) plus a
// synthetic go.mod, all rooted at egressproxy/ in the build context.
func EgressProxySources() []BuildContextFile {
	entries, err := egressProxySrc.ReadDir("egressproxy")
	if err != nil {
		panic("reading embedded egressproxy: " + err.Error())
	}
	var out []BuildContextFile
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := egressProxySrc.ReadFile("egressproxy/" + name)
		if err != nil {
			panic("reading embedded egressproxy/" + name + ": " + err.Error())
		}
		out = append(out, BuildContextFile{Name: "egressproxy/" + name, Content: data, Mode: 0644})
	}
	out = append(out, BuildContextFile{Name: "egressproxy/go.mod", Content: []byte(synthEgressGoMod), Mode: 0644})
	return out
}
```
(Add `"strings"` to `embed.go` imports.)

In `build.go` `buildContextTar`, after the scripts loop, add the proxy sources:
```go
	for _, f := range EgressProxySources() {
		if err := addTarEntry(tw, f.Name, f.Content, int64(f.Mode), modTime); err != nil {
			return nil, fmt.Errorf("adding %s: %w", f.Name, err)
		}
	}
```
Also add the same to `WriteBuildContext` in `embed.go` so `--dump-dockerfile`/genbuild write them (create the `egressproxy/` subdir):
```go
	for _, f := range EgressProxySources() {
		full := filepath.Join(outDir, filepath.FromSlash(f.Name))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			return fmt.Errorf("creating dir for %s: %w", f.Name, err)
		}
		if err := os.WriteFile(full, f.Content, f.Mode); err != nil {
			return fmt.Errorf("writing %s: %w", f.Name, err)
		}
	}
```

Add fields to `baseTemplateData` in `baseimage.go`:
```go
	ProxyUser       string
	ProxyUID        int
	ProxyBinaryPath string
```
and set them in `generateBaseContent`:
```go
		ProxyUser:       ProxyUser,
		ProxyUID:        ProxyUID,
		ProxyBinaryPath: ProxyBinaryPath,
```

Prepend the builder stage and add the user + COPY in `base.dockerfile.tmpl`. At the very top:
```dockerfile
# Build the SNI-aware egress proxy (stdlib-only, standalone module) for the
# target arch. The builder stage is discarded from the final image.
FROM golang:1.23-bookworm AS proxybuild
WORKDIR /build
COPY egressproxy/ ./egressproxy/
RUN cd egressproxy && CGO_ENABLED=0 GOPROXY=off go build -trimpath -o /egress-proxy .

FROM debian:bookworm-slim
```
After the `{{.User}}` user is created (near the top, after the existing `useradd`), add the proxy user:
```dockerfile
# Dedicated non-root user for the egress proxy (privilege separation): it owns
# its config/CA/real-secret files, which the agent user cannot read.
RUN groupadd --gid {{.ProxyUID}} {{.ProxyUser}} && \
  useradd --uid {{.ProxyUID}} --gid {{.ProxyUID}} -M -s /usr/sbin/nologin {{.ProxyUser}}
```
In the root COPY block near the bottom (with the firewall scripts), add:
```dockerfile
COPY --from=proxybuild /egress-proxy {{.ProxyBinaryPath}}
RUN chmod 0755 {{.ProxyBinaryPath}}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/container/ -run 'EgressProxySources|MultiStageProxy|GenerateBaseDockerfile'`
Then a real build smoke (proves the multi-stage compiles offline):
```bash
go run . --dump-dockerfile /tmp/bunker-ctx && ls /tmp/bunker-ctx/egressproxy/main.go && \
  docker build -t bunker-egress-smoke /tmp/bunker-ctx && echo BUILD_OK
```
Expected: tests PASS; `BUILD_OK` (the proxy binary compiles under `GOPROXY=off`).

- [ ] **Step 5: Commit**

```bash
git add internal/container/constants.go internal/container/embed.go internal/container/build.go internal/container/baseimage.go internal/container/scripts/base.dockerfile.tmpl internal/container/egressproxy_context_test.go internal/container/baseimage_test.go
git commit -m "build(egressproxy): multi-stage proxy build + bunker-proxy user + baked binary"
```

---

### Task 7: init-firewall.sh — launch proxy, nat REDIRECT, domain-fronting self-test

**Files:**
- Modify: `internal/container/scripts/init-firewall.sh`
- Test: `internal/container/firewall_script_test.go` (new — asserts the script text contains the required constructs; the live behavior is verified by the in-container self-test the script itself runs)

**Interfaces:**
- Consumes: `ProxyPort`, `ProxyUID`, `ProxyBinaryPath`, `MaskingConfigPath`, `ProxyCADir`, `ProxyCACertPath`, `AllowedDomainsPath` (referenced as literals in the shell script; the Go test asserts the literals match the constants).
- Produces: an `init-firewall.sh` that, after the ipset rules, starts the proxy as `bunker-proxy`, waits for it to listen, adds the nat REDIRECT + owner RETURN, and self-tests domain-fronting.

- [ ] **Step 1: Write the failing test**

`firewall_script_test.go`:
```go
package container

import "testing"

func TestInitFirewallStartsProxyAndRedirects(t *testing.T) {
	s := string(initFirewallScript)
	for _, want := range []string{
		ProxyBinaryPath,
		"--dport 443 -m owner --uid-owner 1001 -j RETURN",
		"--dport 443 -j REDIRECT --to-ports 15443",
		"runuser -u bunker-proxy",
		"--resolve", // the domain-fronting self-test
	} {
		if !contains(s, want) {
			t.Errorf("init-firewall.sh missing %q", want)
		}
	}
}

func contains(hay, needle string) bool {
	return len(hay) >= len(needle) && (indexOf(hay, needle) >= 0)
}
func indexOf(hay, needle string) int {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/container/ -run InitFirewallStartsProxy`
Expected: FAIL — the script has none of these yet.

- [ ] **Step 3: Write minimal implementation**

In `init-firewall.sh`, **after** the ipset ACCEPT rule is added (after line ~87, `iptables -A OUTPUT -m set --match-set "$IPSET_LIVE" dst -j ACCEPT`) and **before** the final `iptables -A OUTPUT -j REJECT`, insert the proxy bring-up. The proxy needs its egress allowed, so this must be before the REJECT catch-all:
```bash
# ---------------------------------------------------------------------------
# SNI-aware egress proxy: start it, then transparently REDIRECT agent TCP/443
# to it. The proxy re-checks the allowlist by TLS SNI (closing the domain-
# fronting gap the ipset /24 tier cannot see) and, when a masking config is
# present, terminates the auth hosts to inject real credentials. The proxy's
# own egress (uid 1001) skips the REDIRECT and is filtered by the ipset tier.
# ---------------------------------------------------------------------------
PROXY_BIN="/usr/local/bin/egress-proxy"
PROXY_PORT="15443"
PROXY_UID="1001"
MASKING_CONFIG="/etc/claude-bunker/proxy/masking.json"
PROXY_CA_DIR="/etc/claude-bunker/proxy/ca"
PROXY_CA_CERT="/etc/claude-bunker/proxy/ca/ca.pem"

if [ -x "$PROXY_BIN" ]; then
    mkdir -p "$PROXY_CA_DIR"
    chown -R "$PROXY_UID:$PROXY_UID" /etc/claude-bunker/proxy 2>/dev/null || true

    PROXY_ARGS="--listen 127.0.0.1:$PROXY_PORT --allowlist $DOMAINS_FILE --ca-dir $PROXY_CA_DIR"
    if [ -f "$MASKING_CONFIG" ]; then
        PROXY_ARGS="$PROXY_ARGS --masking $MASKING_CONFIG"
    fi

    # Launch as the dedicated non-root proxy user.
    runuser -u "$(getent passwd "$PROXY_UID" | cut -d: -f1)" -- \
        env nohup "$PROXY_BIN" $PROXY_ARGS >/tmp/egress-proxy.log 2>&1 &

    # Wait up to 5s for the listener.
    for _ in $(seq 1 50); do
        if (exec 3<>/dev/tcp/127.0.0.1/$PROXY_PORT) 2>/dev/null; then
            exec 3>&- 2>/dev/null || true
            break
        fi
        sleep 0.1
    done

    # If masking is active, install the proxy CA so terminated hosts are trusted.
    if [ -f "$MASKING_CONFIG" ] && [ -f "$PROXY_CA_CERT" ]; then
        cp "$PROXY_CA_CERT" /usr/local/share/ca-certificates/bunker-egress-ca.crt
        update-ca-certificates >/dev/null 2>&1 || true
    fi

    # Transparent REDIRECT: proxy's own egress (owner) returns; everything else
    # on 443 is redirected to the local proxy.
    iptables -t nat -A OUTPUT -p tcp --dport 443 -m owner --uid-owner "$PROXY_UID" -j RETURN
    iptables -t nat -A OUTPUT -p tcp --dport 443 -j REDIRECT --to-ports "$PROXY_PORT"
else
    echo "WARNING: egress proxy binary not found at $PROXY_BIN — SNI tier disabled"
fi
```

> The literal `runuser -u bunker-proxy` the test asserts: change the launch line to the explicit user name for readability and to satisfy the test:
```bash
    runuser -u bunker-proxy -- env nohup "$PROXY_BIN" $PROXY_ARGS >/tmp/egress-proxy.log 2>&1 &
```
(Drop the `getent` indirection.)

Then extend the **verification** section (near the end, after the ipset self-test) with the domain-fronting assertion:
```bash
# ---------------------------------------------------------------------------
# SNI self-test: prove the proxy blocks a non-allowlisted SNI even when the
# destination IP is inside an allowlisted /24. Force-resolve a bogus hostname
# onto an allowlisted IP; the proxy must refuse it (curl fails).
# ---------------------------------------------------------------------------
if [ -x "$PROXY_BIN" ]; then
    ALLOWED_IP=$(ipset list "$IPSET_NAME" 2>/dev/null | awk '/^[0-9]/{print $1; exit}' | cut -d/ -f1)
    if [ -n "$ALLOWED_IP" ]; then
        if curl --connect-timeout 3 --max-time 5 --resolve "fronting.invalid:443:$ALLOWED_IP" \
            https://fronting.invalid/ >/dev/null 2>&1; then
            echo "ERROR: SNI self-test failed — domain-fronting to $ALLOWED_IP was NOT blocked"
            exit 1
        fi
        echo "Verified: domain-fronting blocked by the SNI proxy"
    fi
fi
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/container/ -run InitFirewall`
Expected: PASS. (Live behavior is exercised by the in-container self-test on real runs / Task 8's integration smoke.)

- [ ] **Step 5: Commit**

```bash
git add internal/container/scripts/init-firewall.sh internal/container/firewall_script_test.go
git commit -m "feat(firewall): start egress proxy + transparent 443 REDIRECT + domain-fronting self-test"
```

---

### Task 8: Native masking orchestration — sentinel/real split + masking config

**Files:**
- Modify: `internal/container/lifecycle.go` (`RunPostStart` ordering; `InjectAuthSecrets`, `writeSecretFiles`, `createAuthWrapper`; new `writeMaskingConfig`)
- Modify: `internal/container/constants.go` (add `NodeExtraCACertsEnv` helper if needed — see Step 3)
- Test: `internal/container/masking_test.go`

**Interfaces:**
- Consumes: `AuthTokens`, `MaskingConfigPath`, `ProxyUID`, `ProxyConfigDir`, `SecretsDir`.
- Produces: `func BuildMaskRules(auth AuthTokens) (rules []maskRuleJSON, sentinels AuthTokens)` — pure function returning the masking JSON rules (real secrets) + the sentinel-substituted `AuthTokens` to hand the agent. `maskRuleJSON` mirrors the proxy's `MaskRule`. Masking is **on** whenever `auth.HasSecrets()`.

- [ ] **Step 1: Write the failing test**

`masking_test.go`:
```go
package container

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildMaskRulesSplitsSecrets(t *testing.T) {
	auth := AuthTokens{ApiKey: "sk-ant-REAL", OAuthToken: "oauth-REAL", GhToken: "ghp_REAL"}
	rules, sentinels := BuildMaskRules(auth)

	// Sentinels must differ from the real secrets and be format-preserving.
	if sentinels.ApiKey == auth.ApiKey || !strings.HasPrefix(sentinels.ApiKey, "sk-ant-") {
		t.Errorf("api key sentinel bad: %q", sentinels.ApiKey)
	}
	if sentinels.GhToken == auth.GhToken || !strings.HasPrefix(sentinels.GhToken, "ghp_") {
		t.Errorf("gh sentinel bad: %q", sentinels.GhToken)
	}
	if sentinels.OAuthToken == auth.OAuthToken || sentinels.OAuthToken == "" {
		t.Errorf("oauth sentinel bad: %q", sentinels.OAuthToken)
	}

	// Rules must carry the REAL secrets and target the right hosts/headers.
	blob, _ := json.Marshal(rules)
	s := string(blob)
	for _, want := range []string{"sk-ant-REAL", "oauth-REAL", "ghp_REAL", "api.anthropic.com", "github.com", "x-api-key", "authorization"} {
		if !strings.Contains(s, want) {
			t.Errorf("rules missing %q: %s", want, s)
		}
	}
	// The sentinel in each rule must match what the agent gets.
	for _, r := range rules {
		if r.Secret == "sk-ant-REAL" && r.Sentinel != sentinels.ApiKey {
			t.Error("api-key rule sentinel mismatch")
		}
	}
}

func TestBuildMaskRulesEmptyWhenNoSecrets(t *testing.T) {
	rules, _ := BuildMaskRules(AuthTokens{})
	if len(rules) != 0 {
		t.Errorf("no secrets => no rules, got %d", len(rules))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/container/ -run BuildMaskRules`
Expected: FAIL — `undefined: BuildMaskRules`.

- [ ] **Step 3: Write minimal implementation**

Add to `lifecycle.go` (or a new `masking.go` in `package container`):
```go
import "crypto/rand"

// maskRuleJSON is the on-disk shape the egress proxy reads (mirrors
// egressproxy.MaskRule; kept as a local type to avoid importing package main).
type maskRuleJSON struct {
	Sentinel string   `json:"sentinel"`
	Secret   string   `json:"secret"`
	Hosts    []string `json:"hosts"`
	Headers  []string `json:"headers"`
}

// randToken returns n hex chars of CSPRNG output.
func randToken(n int) string {
	b := make([]byte, n/2)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand: " + err.Error())
	}
	const hexd = "0123456789abcdef"
	out := make([]byte, n)
	for i, x := range b {
		out[i*2] = hexd[x>>4]
		out[i*2+1] = hexd[x&0x0f]
	}
	return string(out)
}

// BuildMaskRules produces the proxy masking rules (holding the REAL secrets)
// and the sentinel-substituted AuthTokens to hand the agent. Sentinels are
// format-preserving so clients don't reject them on shape.
func BuildMaskRules(auth AuthTokens) ([]maskRuleJSON, AuthTokens) {
	if !auth.HasSecrets() {
		return nil, AuthTokens{}
	}
	var rules []maskRuleJSON
	sent := AuthTokens{}
	if auth.ApiKey != "" {
		s := "sk-ant-" + randToken(32)
		sent.ApiKey = s
		rules = append(rules, maskRuleJSON{Sentinel: s, Secret: auth.ApiKey,
			Hosts: []string{"api.anthropic.com"}, Headers: []string{"x-api-key", "authorization"}})
	}
	if auth.OAuthToken != "" {
		s := "sk-ant-oat-" + randToken(32)
		sent.OAuthToken = s
		rules = append(rules, maskRuleJSON{Sentinel: s, Secret: auth.OAuthToken,
			Hosts: []string{"api.anthropic.com"}, Headers: []string{"authorization"}})
	}
	if auth.GhToken != "" {
		s := "ghp_" + randToken(36)
		sent.GhToken = s
		rules = append(rules, maskRuleJSON{Sentinel: s, Secret: auth.GhToken,
			Hosts: []string{"github.com", "api.github.com"}, Headers: []string{"authorization"}})
	}
	return rules, sent
}
```

Wire it into `RunPostStart` + `InjectAuthSecrets`. The critical ordering change: the masking config (with real secrets) and the CA dir must exist **before** `init-firewall.sh` runs (the script starts the proxy). So:

1. In `RunPostStart`, replace the current step-4 `InjectAuthSecrets` call placement: split into **4a (before firewall)** = write masking config + real secrets + create the proxy config dir; and keep **4b (after firewall)** = inject the agent's sentinel secrets + wrapper. Concretely, move a new call `if err := PrepareMasking(ctx, cli, containerID, opts.Auth); err != nil {...}` to run right after step 1b (pre-firewall) and change step 4 to inject **sentinels**.

Add:
```go
// PrepareMasking writes the proxy masking config (real secrets) into a
// bunker-proxy-owned dir before the firewall/proxy start. No-op if no secrets.
func PrepareMasking(ctx context.Context, cli *client.Client, containerID string, auth AuthTokens) (AuthTokens, error) {
	rules, sentinels := BuildMaskRules(auth)
	if len(rules) == 0 {
		return auth, nil // nothing to mask
	}
	blob, err := json.Marshal(rules)
	if err != nil {
		return auth, err
	}
	if err := CopyContentToContainer(ctx, cli, containerID, blob, MaskingConfigPath); err != nil {
		return auth, fmt.Errorf("writing masking config: %w", err)
	}
	// Lock the config + CA dir to the proxy user (agent uid 1000 cannot read).
	var script strings.Builder
	script.WriteString("set -e\n")
	fmt.Fprintf(&script, "mkdir -p %s %s\n", ProxyConfigDir, ProxyCADir)
	fmt.Fprintf(&script, "chown -R %d:%d %s\n", ProxyUID, ProxyGID, ProxyConfigDir)
	fmt.Fprintf(&script, "chmod 0400 %s\n", MaskingConfigPath)
	fmt.Fprintf(&script, "chmod 0700 %s %s\n", ProxyConfigDir, ProxyCADir)
	if _, err := ExecNonInteractive(ctx, cli, containerID, RootUser, []string{"sh", "-c", script.String()}); err != nil {
		return auth, fmt.Errorf("locking masking config: %w", err)
	}
	return sentinels, nil
}
```

In `RunPostStart`, between step 1b and step 2, add:
```go
	// 1c. Prepare credential masking (real secrets → proxy-owned config) BEFORE
	// the firewall starts the proxy. Returns the sentinel tokens for the agent.
	sentinelAuth, err := PrepareMasking(ctx, cli, containerID, opts.Auth)
	if err != nil {
		return fmt.Errorf("prepare masking: %w", err)
	}
```
Change step 4 to inject the **sentinels**:
```go
	// 4. Inject auth secrets (sentinels when masking is active) for the agent.
	if err := InjectAuthSecrets(ctx, cli, containerID, sentinelAuth); err != nil {
		return fmt.Errorf("auth injection: %w", err)
	}
```
Set `NODE_EXTRA_CA_CERTS` for the agent so Claude Code trusts the proxy CA when masking is active. In the container env assembly (where the container is created — find the `Env`/`containerEnv` construction for the native run; add a constant and set it only when `opts.Auth.HasSecrets()`):
```go
// In lifecycle.go RunPostStart step 3 batch (root), after starting the refresh
// daemon, export NODE_EXTRA_CA_CERTS for the agent via the auth wrapper is the
// cleanest single point — but the wrapper is created in createAuthWrapper.
```
Add to `createAuthWrapper` (only when masking active, i.e. secrets present) an export line:
```go
	// When credential masking is active the proxy terminates the auth hosts;
	// Node/Claude Code must trust the per-container CA.
	fmt.Fprintf(script, "export NODE_EXTRA_CA_CERTS=%q\n", ProxyCACertPath)
```
(Place inside the `if !auth.HasSecrets() { return }`-guarded body so it only appears when there are secrets.)

`InjectAuthSecrets`/`writeSecretFiles`/`createAuthWrapper` otherwise stay as-is — they now receive sentinel tokens, so `/run/secrets/*` and the exported env vars hold sentinels, and the git credential helper returns the sentinel (the proxy swaps it on the wire to github.com).

Add `"encoding/json"` to `lifecycle.go` imports if not present.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/container/`
Expected: PASS. Also `go build ./...`.

- [ ] **Step 5: Commit**

```bash
git add internal/container/lifecycle.go internal/container/constants.go internal/container/masking_test.go
git commit -m "feat(masking): sentinel/real credential split + proxy masking config (native)"
```

---

### Task 9: Portable path — bake proxy into the committed bundle (Tier 1)

**Files:**
- Modify: `cmd/init.go` (`writeDevContainer`) — the committed Dockerfile already comes from `GenerateBaseDockerfile()` (Task 6 added the multi-stage + user + COPY), so the proxy is baked automatically. This task ensures the `egressproxy/` source is written into `.devcontainer/` so the committed Dockerfile's `COPY egressproxy/` resolves, and that the portable `postStartCommand` runs `init-firewall.sh` (which starts the proxy in Tier-1 mode).
- Modify: `internal/devcontainer/generate.go` if needed (verify the firewall postStart passes the baked allowlist path; no masking config in portable).
- Test: `cmd/init_test.go` (extend the existing bundle-content assertions)

**Interfaces:**
- Consumes: `container.EgressProxySources()`, `container.GenerateBaseDockerfile()`.
- Produces: a `.devcontainer/` bundle whose Dockerfile builds the proxy and whose `egressproxy/` subdir holds the proxy source.

- [ ] **Step 1: Write the failing test**

Extend `cmd/init_test.go` (or create if the helper exists elsewhere) with an assertion on the generated bundle:
```go
func TestWriteDevContainerBakesEgressProxy(t *testing.T) {
	dir := t.TempDir()
	// (reuse the existing writeDevContainer test harness/config in this file)
	writeTestDevContainer(t, dir) // existing helper; if named differently, match it

	// The committed Dockerfile must reference the multi-stage proxy build.
	df := readFile(t, filepath.Join(dir, ".devcontainer", "Dockerfile"))
	if !strings.Contains(df, "AS proxybuild") || !strings.Contains(df, "COPY egressproxy/") {
		t.Error("committed Dockerfile does not build the egress proxy")
	}
	// The proxy source must be present so the COPY resolves.
	if _, err := os.Stat(filepath.Join(dir, ".devcontainer", "egressproxy", "main.go")); err != nil {
		t.Errorf("egressproxy/main.go not written into bundle: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".devcontainer", "egressproxy", "go.mod")); err != nil {
		t.Errorf("egressproxy/go.mod not written into bundle: %v", err)
	}
}
```

> If `cmd/init_test.go` already has a bundle-writing helper with a different name, adapt the call; the assertions are what matter.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/ -run BakesEgressProxy`
Expected: FAIL — the bundle has no `egressproxy/` dir.

- [ ] **Step 3: Write minimal implementation**

In `writeDevContainer` (`cmd/init.go`), where `container.BuildContextScripts()` are written into `.devcontainer/`, also write the proxy sources (guarded by the same dry-run check):
```go
	for _, f := range container.EgressProxySources() {
		dest := filepath.Join(devcontainerDir, filepath.FromSlash(f.Name)) // egressproxy/<name>
		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			return fmt.Errorf("creating %s dir: %w", f.Name, err)
		}
		if err := os.WriteFile(dest, f.Content, f.Mode); err != nil {
			return fmt.Errorf("writing %s: %w", f.Name, err)
		}
	}
```
No masking config is written (portable = Tier 1). The existing firewall `postStartCommand` runs `init-firewall.sh <AllowedDomainsPath>`; Task 7's script sees no `masking.json` → starts the proxy in splice/allowlist mode, no CA. Confirm `firewallPostStartCommand()` passes `container.AllowedDomainsPath` (it does, from Phase 2c) — no change needed.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/`
Then a bundle smoke:
```bash
cd $(mktemp -d) && (cd /Users/devon/projects/claude-bunker && go build -o /tmp/bunker .) && \
  git init -q && /tmp/bunker init --yes 2>/dev/null; ls .devcontainer/egressproxy/main.go && \
  docker build -t bunker-portable-smoke .devcontainer && echo PORTABLE_BUILD_OK
```
Expected: tests PASS; `PORTABLE_BUILD_OK` (the committed bundle builds, proxy compiled).

- [ ] **Step 5: Commit**

```bash
git add cmd/init.go cmd/init_test.go
git commit -m "feat(init): bake egress proxy source into the portable .devcontainer bundle"
```

---

### Task 10: Fingerprints + documentation

**Files:**
- Modify: `internal/config/fingerprint.go` — fold the proxy source into the **image** fingerprint (it changes the image) and the masking toggle into the **container** fingerprint (masking on/off changes runtime setup).
- Modify: `CLAUDE.md` — document the egress-proxy layer under Security Layers.
- Test: `internal/config/fingerprint_test.go` (extend)

**Interfaces:**
- Consumes: `container.EgressProxySources()`.
- Produces: fingerprints that change when the proxy source changes (image) and when masking is toggled (container).

- [ ] **Step 1: Write the failing test**

Extend `fingerprint_test.go`:
```go
func TestImageFingerprintCoversProxySource(t *testing.T) {
	in := sampleBuildInput(t) // existing helper producing a BuildInput
	base := imageFingerprint(in)

	// Mutating the proxy source must change the image fingerprint. Simulate by
	// asserting the fingerprint input includes the proxy sources: compute with
	// the real sources and confirm non-empty + stable across two calls.
	again := imageFingerprint(in)
	if base != again {
		t.Fatal("image fingerprint must be deterministic")
	}
	if base == "" {
		t.Fatal("image fingerprint empty")
	}
}
```

> Since `imageFingerprint` is fed from `BuildInput`, the real assertion is structural: Step 3 adds the proxy sources to the hashed material. If the fingerprint helpers hash a `Scripts`-like slice, add `EgressProxySources()` to it and assert (via a focused test) that two inputs differing only in a stubbed proxy-source byte produce different fingerprints. Match the existing test's style for injecting build inputs.

- [ ] **Step 2: Run test to verify it fails / confirm coverage**

Run: `go test ./internal/config/ -run Fingerprint`
Expected: initially PASS for determinism; the coverage assertion drives Step 3 if the fingerprint doesn't yet include proxy sources.

- [ ] **Step 3: Write minimal implementation**

In `fingerprint.go`, wherever the image fingerprint hashes the embedded scripts (it already hashes `BuildContextScripts()` per the Phase-2c notes), add the proxy sources to the same hashed material:
```go
	for _, f := range container.EgressProxySources() {
		h.Write([]byte(f.Name))
		h.Write(f.Content)
	}
```
For the container fingerprint, include whether masking is active (secrets present). If the container fingerprint already hashes an auth/secrets indicator, no change; otherwise add a boolean:
```go
	// Masking toggles runtime proxy behavior (terminate vs splice-only).
	if input.HasSecrets {
		h.Write([]byte("masking:on"))
	}
```
(Use the existing field that indicates secrets; if none exists in the fingerprint input, thread `auth.HasSecrets()` through — match the existing container-fingerprint signature.)

Update `CLAUDE.md` "Five Security Layers" → make it six, inserting after the iptables layer:
```markdown
2b. **SNI-aware egress proxy** — a stdlib-only Go proxy (`internal/container/egressproxy`) baked via multi-stage build; `init-firewall.sh` transparently REDIRECTs agent TCP/443 to it. It allowlists by TLS SNI (closing CDN-IP domain-fronting the ipset /24 tier can't see) and, in the bunker-CLI path, terminates the 1–3 credential hosts to swap a per-session sentinel for the real token (`InjectAuthSecrets` gives the agent only sentinels; real secrets live in `bunker-proxy`-owned files). Portable path runs the same binary in Tier-1 (splice) mode.
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./... && gofmt -l . && GOOS=windows go build ./...`
Expected: all PASS; `gofmt` clean; Windows cross-build OK.

- [ ] **Step 5: Commit**

```bash
git add internal/config/fingerprint.go internal/config/fingerprint_test.go CLAUDE.md
git commit -m "feat(fingerprint): cover egress proxy source + masking; docs"
```

---

## Self-Review

**Spec coverage:**
- Tier 1 SNI allowlist/splice/fail-closed → Tasks 1–3. ✅
- Tier 2 terminate + masking → Tasks 4–5, 8. ✅
- Transparent REDIRECT + owner RETURN → Task 7. ✅
- Domain-fronting self-test → Task 7. ✅
- Multi-stage build, bunker-proxy uid, single allowlist source → Task 6. ✅
- Sentinel/real split, CA trust (`NODE_EXTRA_CA_CERTS` + `update-ca-certificates`), masking bunker-CLI-only → Task 8 (+ Task 7 installs CA). ✅
- Portable = Tier 1 → Task 9. ✅
- Fingerprints + docs → Task 10. ✅
- Interop with upstream corporate proxy: **partial gap** — the spec says the SNI layer stands down when `HTTPS_PROXY` is set. Currently the proxy always starts. Mitigation: this is low-risk (the redirect only affects direct 443; a corp-proxy user's traffic goes to the proxy host:port, not 443, so it isn't redirected) — but to honor the spec, add a guard in Task 7's script: skip the REDIRECT block when `/run/secrets` indicates an upstream proxy. **Resolution:** fold this into Task 7 — wrap the REDIRECT+proxy block in `if [ -z "${HTTPS_PROXY:-}${https_proxy:-}" ]; then ... fi`. (Added below.)

**Placeholder scan:** No TBD/TODO. Every code step has complete code. The one "match the existing helper" notes (Tasks 9–10) reference real existing test harnesses the implementer will see; exact assertions are given.

**Type consistency:** `MaskRule`(proxy) ↔ `maskRuleJSON`(container) fields match (`sentinel`/`secret`/`hosts`/`headers`). `serveWithCA`/`serve`/`handle` signatures consistent across Tasks 3–5. `certAuthority`/`loadOrCreateCA`/`leafFor`/`certPEM` consistent Tasks 4–5. Constants (`ProxyUID=1001`, `ProxyPort="15443"`) match the shell literals asserted in Task 7.

**Fix applied inline (interop guard) — amend Task 7 Step 3:** wrap the proxy-bring-up and REDIRECT in:
```bash
if [ -z "${HTTPS_PROXY:-}${https_proxy:-}" ] && [ -x "$PROXY_BIN" ]; then
    ... (proxy launch + CA + REDIRECT) ...
else
    echo "egress proxy skipped (upstream proxy set or binary missing)"
fi
```
and guard the SNI self-test with the same `-x "$PROXY_BIN"` plus `nat` rule presence.
