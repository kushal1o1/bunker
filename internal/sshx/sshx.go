package sshx

import (
	"bytes"
	"context"
	"errors"
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
	// Timeout bounds the TCP connect and the SSH handshake, separately.
	// Zero means DefaultTimeout.
	Timeout time.Duration
}

// DefaultTimeout is used when Config.Timeout is zero.
const DefaultTimeout = 10 * time.Second

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
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
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
	where := cfg.User + "@" + addr

	dialer := net.Dialer{Timeout: cfg.Timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("ssh %s: %w", where, ctx.Err())
		}
		if isTimeout(err) {
			return nil, fmt.Errorf("ssh %s: no response after %s (host down, wrong IP, or a firewall dropping packets)", where, cfg.Timeout)
		}
		return nil, fmt.Errorf("ssh %s: %w", where, err)
	}

	// x/crypto/ssh has no context support, so the handshake is bounded two
	// ways: a deadline on the socket for a peer that accepts the connection
	// but never speaks SSH, and closing the socket if ctx is cancelled
	// (Ctrl-C) mid-handshake. Both are lifted once the handshake is done.
	conn.SetDeadline(time.Now().Add(cfg.Timeout))
	stop := context.AfterFunc(ctx, func() { conn.Close() })

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, clientCfg)
	if !stop() {
		// ctx was cancelled and the socket is already closed.
		if sshConn != nil {
			sshConn.Close()
		}
		return nil, fmt.Errorf("ssh %s: %w", where, ctx.Err())
	}
	if err != nil {
		conn.Close()
		if isTimeout(err) {
			return nil, fmt.Errorf("ssh %s: connected, but no SSH handshake within %s (is an SSH server listening on this port?)", where, cfg.Timeout)
		}
		return nil, fmt.Errorf("ssh %s: %w", where, err)
	}
	conn.SetDeadline(time.Time{})
	return &Client{conn: ssh.NewClient(sshConn, chans, reqs)}, nil
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
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

// maxStderr bounds how much remote stderr is buffered for an error message.
// The buffer is only ever read when a command fails, and a chatty command
// must not be able to grow it without limit.
const maxStderr = 64 << 10

// limitedBuffer keeps the first maxStderr bytes and discards the rest, so a
// runaway remote command cannot exhaust memory while still reporting why it
// failed.
type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int
	dropped   int
	truncated bool
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.limit - l.buf.Len(); room > 0 {
		if len(p) > room {
			l.buf.Write(p[:room])
			l.dropped = len(p) - room
			l.truncated = true
			return len(p), nil
		}
		l.buf.Write(p)
		return len(p), nil
	}
	l.dropped += len(p)
	l.truncated = true
	return len(p), nil
}

func (l *limitedBuffer) String() string {
	if !l.truncated {
		return l.buf.String()
	}
	return l.buf.String() + fmt.Sprintf("\n... [%d more bytes of remote stderr truncated]", l.dropped)
}

type Process struct {
	ctx    context.Context
	sess   *ssh.Session
	Out    io.Reader
	stderr limitedBuffer
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

	p := &Process{
		ctx:   ctx,
		sess:  sess,
		Out:   out,
		errCh: make(chan error, 1),
	}
	p.stderr.limit = maxStderr
	// Must point at the Process field: a throwaway buffer here would discard
	// every diagnostic a failing remote command emits, which is the only
	// clue about why it failed.
	p.sess.Stderr = &p.stderr

	if err := sess.Start(command); err != nil {
		sess.Close()
		return nil, fmt.Errorf("start remote command: %w", err)
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
