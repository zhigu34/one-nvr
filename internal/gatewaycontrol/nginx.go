package gatewaycontrol

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"github.com/zhigu34/one-nvr/deploy/production/templates"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/tlsmanager"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Nginx struct {
	Directory, VersionsDirectory, PublicURL, ProxyToken string
	selected                                            id.ID
	prepared                                            id.ID
}

func (n *Nginx) command(ctx context.Context, args ...string) error {
	run, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(run, "/usr/sbin/nginx", args...)
	// Nginx diagnostics may include config paths; no raw output or private
	// configuration is copied to API diagnostics or application logs.
	return command.Run()
}
func (n *Nginx) Prepare(ctx context.Context, target id.ID) (Evidence, error) {
	return n.prepare(ctx, target, false)
}
func (n *Nginx) PrepareHistorical(ctx context.Context, target id.ID) (Evidence, error) {
	return n.prepare(ctx, target, true)
}
func (n *Nginx) prepare(ctx context.Context, target id.ID, historical bool) (Evidence, error) {
	var evidence Evidence
	parsed, err := url.Parse(n.PublicURL)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return evidence, ErrInvalid
	}
	token, err := hex.DecodeString(n.ProxyToken)
	if err != nil || len(token) != 32 {
		return evidence, ErrInvalid
	}
	config := templates.Pending
	if parsed.Scheme == "http" {
		if target != "" {
			return evidence, ErrInvalid
		}
		config = templates.HTTP
	}
	if parsed.Scheme == "https" && target != "" {
		meta, err := tlsmanager.ReadSnapshot(n.VersionsDirectory, target, parsed.Hostname(), time.Now(), historical)
		if err != nil {
			return evidence, err
		}
		evidence = Evidence{meta.LeafSHA256, meta.ChainSHA256}
		config = strings.ReplaceAll(templates.HTTPS, "__CERTIFICATE_ID__", string(target))
	}
	config = strings.ReplaceAll(config, "__PROXY_TOKEN__", n.ProxyToken)
	root, err := controlRoot(n.Directory)
	if err != nil {
		return evidence, err
	}
	defer root.Close()
	if err = root.MkdirAll("tmp", 0700); err != nil {
		return evidence, err
	}
	if err = writeFile(ctx, root, "candidate.conf", []byte(config)); err != nil {
		return evidence, err
	}
	if err = n.command(ctx, "-t", "-c", filepath.Join(n.Directory, "candidate.conf")); err != nil {
		return evidence, err
	}
	n.prepared = target
	return evidence, nil
}
func (n *Nginx) Switch(ctx context.Context, target id.ID) error {
	if target != n.prepared {
		return ErrConflict
	}
	root, err := controlRoot(n.Directory)
	if err != nil {
		return err
	}
	defer root.Close()
	data, err := root.ReadFile("candidate.conf")
	if err != nil {
		return err
	}
	if err = writeFile(ctx, root, "active.conf", data); err != nil {
		return err
	}
	n.selected = target
	return nil
}
func (n *Nginx) Reload(ctx context.Context) error {
	return n.command(ctx, "-s", "reload", "-c", filepath.Join(n.Directory, "active.conf"))
}
func (n *Nginx) Probe(ctx context.Context) (Evidence, error) {
	if n.selected == "" {
		// No certificate listener is expected in the pending bootstrap. A listener
		// still serving any TLS certificate is not evidence of successful removal.
		dial, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		conn, err := (&net.Dialer{}).DialContext(dial, "tcp", "127.0.0.1:443")
		if err == nil {
			conn.Close()
			return Evidence{}, ErrFailed
		}
		if dial.Err() != nil {
			return Evidence{}, ErrFailed
		}
		return Evidence{}, nil
	}
	parsed, err := url.Parse(n.PublicURL)
	if err != nil {
		return Evidence{}, err
	}
	connect, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	// Matching the exact expected certificate/chain is the trust check here;
	// system trust roots would incorrectly reject an approved self-signed pair.
	conn, err := (&tls.Dialer{NetDialer: &net.Dialer{Timeout: 2 * time.Second}, Config: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: parsed.Hostname(), InsecureSkipVerify: true}}).DialContext(connect, "tcp", "127.0.0.1:443")
	if err != nil {
		return Evidence{}, err
	}
	defer conn.Close()
	certificates := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certificates) == 0 {
		return Evidence{}, ErrFailed
	}
	leaf := sha256.Sum256(certificates[0].Raw)
	chain := make([][]byte, 0, len(certificates))
	for _, cert := range certificates {
		chain = append(chain, cert.Raw)
	}
	return Evidence{LeafSHA256: hex.EncodeToString(leaf[:]), ChainSHA256: tlsmanager.ChainFingerprint(chain)}, nil
}

func (n *Nginx) Start(ctx context.Context) (*exec.Cmd, error) {
	command := exec.Command("/usr/sbin/nginx", "-c", filepath.Join(n.Directory, "active.conf"), "-g", "daemon off;")
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, err
	}
	return command, nil
}
