package sshx

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type Config struct {
	Host       string
	Port       int
	User       string
	KeyPath    string
	KnownHosts string
	// Passphrase for an encrypted private key. Empty for unencrypted keys.
	Passphrase string
}

type Client struct {
	conn *ssh.Client
}

func Connect(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Host == "" || cfg.User == "" {
		return nil, fmt.Errorf("host and user are required")
	}
	if cfg.Port == 0 {
		cfg.Port = 22
	}

	key, err := loadKey(cfg.KeyPath, cfg.Passphrase)
	if err != nil {
		return nil, err
	}

	hostKeyCB, err := knownhosts.New(cfg.KnownHosts)
	if err != nil {
		return nil, fmt.Errorf("known_hosts %q: %w", cfg.KnownHosts, err)
	}

	clientCfg := &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(key)},
		HostKeyCallback: hostKeyCB,
	}

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	dialer := net.Dialer{Timeout: 30 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("ssh %s@%s: %w", cfg.User, addr, err)
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, clientCfg)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("ssh %s@%s: %w", cfg.User, addr, err)
	}
	return &Client{conn: ssh.NewClient(sshConn, chans, reqs)}, nil
}

func loadKey(path, passphrase string) (ssh.Signer, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read ssh key %q: %w", path, err)
	}
	if passphrase != "" {
		signer, err := ssh.ParsePrivateKeyWithPassphrase(pem, []byte(passphrase))
		if err != nil {
			return nil, fmt.Errorf("parse encrypted ssh key %q: %w", path, err)
		}
		return signer, nil
	}
	signer, err := ssh.ParsePrivateKey(pem)
	if err != nil {
		return nil, fmt.Errorf("parse ssh key %q: %w", path, err)
	}
	return signer, nil
}

type Process struct {
	ctx    context.Context
	sess   *ssh.Session
	Out    io.Reader
	stderr bytes.Buffer
	errCh  chan error
}

// Exec starts a remote command and streams its stdout.
// Only explicit commands should ever be passed here; this is the single
// choke point through which all remote execution flows.
func (c *Client) Exec(ctx context.Context, command string) (*Process, error) {
	sess, err := c.conn.NewSession()
	if err != nil {
		return nil, fmt.Errorf("open ssh session: %w", err)
	}

	out, err := sess.StdoutPipe()
	if err != nil {
		sess.Close()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	sess.Stderr = &bytes.Buffer{}

	if err := sess.Start(command); err != nil {
		sess.Close()
		return nil, fmt.Errorf("start remote command: %w", err)
	}

	p := &Process{
		ctx:   ctx,
		sess:  sess,
		Out:   out,
		errCh: make(chan error, 1),
	}
	go func() {
		p.errCh <- sess.Wait()
	}()
	return p, nil
}

// Wait blocks until the remote command has finished, then returns its exit
// status. It also reacts to context cancellation by killing the session.
func (p *Process) Wait() error {
	select {
	case <-p.ctx.Done():
		p.sess.Close()
		return fmt.Errorf("cancelled: %w", p.ctx.Err())
	case err := <-p.errCh:
		if err != nil {
			detail := strings.TrimSpace(p.stderr.String())
			if exitErr, ok := err.(*ssh.ExitError); ok {
				if detail != "" {
					return fmt.Errorf("remote exit %d: %s", exitErr.ExitStatus(), detail)
				}
				return fmt.Errorf("remote exit %d", exitErr.ExitStatus())
			}
			return fmt.Errorf("remote command failed: %v (%s)", err, detail)
		}
		return nil
	}
}

func (p *Process) Kill() error {
	return p.sess.Close()
}

func (c *Client) Close() error {
	return c.conn.Close()
}