package sandbox

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/docker/docker/client"

	"github.com/Devon-White/claude-bunker/internal/container"
)

// ProxyConfig holds proxy-related configuration detected from the host environment.
type ProxyConfig struct {
	HTTPSProxy          string // HTTPS_PROXY value
	HTTPProxy           string // HTTP_PROXY value
	NoProxy             string // NO_PROXY value
	NodeExtraCACerts    string // host path to extra CA certs
	ClientCert          string // host path to client cert
	ClientKey           string // host path to client key
	ClientKeyPassphrase string // passphrase for client key
	ProxyHost           string // extracted hostname from proxy URL for firewall
}

// HasCerts returns true if any certificate files need to be injected.
func (p ProxyConfig) HasCerts() bool {
	return p.NodeExtraCACerts != "" || p.ClientCert != "" || p.ClientKey != ""
}

// HasProxy returns true if any proxy configuration was detected.
func (p ProxyConfig) HasProxy() bool {
	return p.HTTPSProxy != "" || p.HTTPProxy != ""
}

// DetectProxyEnv reads proxy-related environment variables from the host.
func DetectProxyEnv() ProxyConfig {
	cfg := ProxyConfig{
		HTTPSProxy:          coalesceEnv("HTTPS_PROXY", "https_proxy"),
		HTTPProxy:           coalesceEnv("HTTP_PROXY", "http_proxy"),
		NoProxy:             coalesceEnv("NO_PROXY", "no_proxy"),
		NodeExtraCACerts:    os.Getenv("NODE_EXTRA_CA_CERTS"),
		ClientCert:          os.Getenv("CLAUDE_CODE_CLIENT_CERT"),
		ClientKey:           os.Getenv("CLAUDE_CODE_CLIENT_KEY"),
		ClientKeyPassphrase: os.Getenv("CLAUDE_CODE_CLIENT_KEY_PASSPHRASE"),
	}

	// Extract proxy hostname for firewall allowlisting
	proxyURL := cfg.HTTPSProxy
	if proxyURL == "" {
		proxyURL = cfg.HTTPProxy
	}
	if proxyURL != "" {
		if host := extractHost(proxyURL); host != "" {
			cfg.ProxyHost = host
		}
	}

	// Validate cert file paths exist — warn if set but missing
	if cfg.NodeExtraCACerts != "" {
		if _, err := os.Stat(cfg.NodeExtraCACerts); err != nil {
			fmt.Fprintf(os.Stderr, "[claude-bunker] WARNING: NODE_EXTRA_CA_CERTS path not found: %s\n", cfg.NodeExtraCACerts)
			cfg.NodeExtraCACerts = ""
		}
	}
	if cfg.ClientCert != "" {
		if _, err := os.Stat(cfg.ClientCert); err != nil {
			fmt.Fprintf(os.Stderr, "[claude-bunker] WARNING: CLAUDE_CODE_CLIENT_CERT path not found: %s\n", cfg.ClientCert)
			cfg.ClientCert = ""
		}
	}
	if cfg.ClientKey != "" {
		if _, err := os.Stat(cfg.ClientKey); err != nil {
			fmt.Fprintf(os.Stderr, "[claude-bunker] WARNING: CLAUDE_CODE_CLIENT_KEY path not found: %s\n", cfg.ClientKey)
			cfg.ClientKey = ""
		}
	}

	return cfg
}

// ProxyContainerEnv returns environment variables to inject into the container
// for proxy support. Cert paths are remapped to container paths.
func ProxyContainerEnv(cfg ProxyConfig) map[string]string {
	env := make(map[string]string)

	if cfg.HTTPSProxy != "" {
		env["HTTPS_PROXY"] = cfg.HTTPSProxy
		env["https_proxy"] = cfg.HTTPSProxy
	}
	if cfg.HTTPProxy != "" {
		env["HTTP_PROXY"] = cfg.HTTPProxy
		env["http_proxy"] = cfg.HTTPProxy
	}
	if cfg.NoProxy != "" {
		env["NO_PROXY"] = cfg.NoProxy
		env["no_proxy"] = cfg.NoProxy
	}
	if cfg.NodeExtraCACerts != "" {
		env["NODE_EXTRA_CA_CERTS"] = containerCertPath(cfg.NodeExtraCACerts)
	}
	if cfg.ClientCert != "" {
		env["CLAUDE_CODE_CLIENT_CERT"] = containerCertPath(cfg.ClientCert)
	}
	if cfg.ClientKey != "" {
		env["CLAUDE_CODE_CLIENT_KEY"] = containerCertPath(cfg.ClientKey)
	}
	// ClientKeyPassphrase is injected as a tmpfs file (not env var) by
	// InjectProxyCerts to avoid exposure via /proc/*/environ and docker inspect.
	// See InjectProxyCerts for the file write at /run/secrets/client_key_passphrase.

	// Forward proxy DNS resolution flag if set on host
	if v := os.Getenv("CLAUDE_CODE_PROXY_RESOLVES_HOSTS"); v != "" {
		env["CLAUDE_CODE_PROXY_RESOLVES_HOSTS"] = v
	}

	return env
}

// ExtractProxyDomain returns the proxy hostname for firewall allowlisting.
func ExtractProxyDomain(cfg ProxyConfig) []string {
	if cfg.ProxyHost == "" {
		return nil
	}
	return []string{cfg.ProxyHost}
}

// InjectProxyCerts reads certificate files from the host and copies them into
// the container at /run/secrets/certs/.
func InjectProxyCerts(ctx context.Context, cli *client.Client, containerID string, cfg ProxyConfig, logW io.Writer) error {
	const certsDir = container.SecretsDir + "/certs"

	// Create certs directory
	if _, err := container.ExecNonInteractive(ctx, cli, containerID, container.RootUser,
		[]string{"mkdir", "-p", certsDir}); err != nil {
		return fmt.Errorf("creating certs dir: %w", err)
	}

	certPaths := []string{
		cfg.NodeExtraCACerts,
		cfg.ClientCert,
		cfg.ClientKey,
	}

	for _, hostPath := range certPaths {
		if hostPath == "" {
			continue
		}
		data, err := os.ReadFile(hostPath)
		if err != nil {
			fmt.Fprintf(logW, "[claude-bunker] WARNING: reading cert %s: %v\n", hostPath, err)
			continue
		}
		basename := filepath.Base(hostPath)
		containerPath := certsDir + "/" + basename
		if err := container.CopyContentToContainer(ctx, cli, containerID, data, containerPath); err != nil {
			return fmt.Errorf("copying cert %s: %w", basename, err)
		}
		fmt.Fprintf(logW, "[claude-bunker] Copied cert %s → %s\n", basename, containerPath)
	}

	// Write client key passphrase to tmpfs file instead of env var to avoid
	// exposure via /proc/*/environ and docker inspect.
	if cfg.ClientKeyPassphrase != "" {
		passphrasePath := container.SecretsDir + "/client_key_passphrase"
		if err := container.CopyContentToContainer(ctx, cli, containerID, []byte(cfg.ClientKeyPassphrase), passphrasePath); err != nil {
			return fmt.Errorf("writing client key passphrase: %w", err)
		}
		// Set permissions: readable only by the container user
		if _, err := container.ExecNonInteractive(ctx, cli, containerID, container.RootUser,
			[]string{"sh", "-c", fmt.Sprintf("chmod 400 %s && chown %s %s", passphrasePath, container.ContainerUserGroup, passphrasePath)}); err != nil {
			fmt.Fprintf(logW, "[claude-bunker] WARNING: chmod passphrase: %v\n", err)
		}
		fmt.Fprintf(logW, "[claude-bunker] Wrote client key passphrase → %s\n", passphrasePath)
	}

	// Fix ownership
	if _, err := container.ExecNonInteractive(ctx, cli, containerID, container.RootUser,
		[]string{"chown", "-R", container.ContainerUserGroup, certsDir}); err != nil {
		fmt.Fprintf(logW, "[claude-bunker] WARNING: chown certs: %v\n", err)
	}

	return nil
}

// coalesceEnv returns the first non-empty environment variable value.
func coalesceEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

// extractHost parses a URL and returns the hostname (without port).
// If parsing fails, attempts to extract host from common proxy formats.
func extractHost(rawURL string) string {
	// Ensure scheme for url.Parse
	if !strings.Contains(rawURL, "://") {
		rawURL = "http://" + rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// containerCertPath returns the container path for a host cert file.
func containerCertPath(hostPath string) string {
	return container.SecretsDir + "/certs/" + filepath.Base(hostPath)
}
