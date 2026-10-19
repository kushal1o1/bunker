package sshx

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// reply is what the fake remote command should emit.
type reply struct {
	stdout string
	stderr string
	exit   uint32
}

// runScript starts an in-process SSH server that answers the exec request with
// the given reply, connects a real Client to it, runs one command and returns
// stdout plus the error from Wait.
func runScript(t *testing.T, r reply) (string, error) {
	t.Helper()

	hostSigner, _, err := generateKey()
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}

	srv := &ssh.ServerConfig{
		PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
			return &ssh.Permissions{}, nil
		},
	}
	srv.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go serveReply(ln, srv, r)

	_, clientPriv, err := generateKey()
	if err != nil {
		t.Fatalf("generate client key: %v", err)
	}
	kb, err := ssh.MarshalPrivateKey(clientPriv, "")
	if err != nil {
		t.Fatalf("marshal client key: %v", err)
	}
	keyPath := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyPath, encodePEM(kb), 0o600); err != nil {
		t.Fatalf("write client key: %v", err)
	}

	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	writeKnownHost(t, knownHosts, ln.Addr().String(), hostSigner.PublicKey())

	c, err := Connect(context.Background(), Config{
		Host:       "127.0.0.1",
		Port:       ln.Addr().(*net.TCPAddr).Port,
		User:       "bunker",
		KeyPath:    keyPath,
		KnownHosts: knownHosts,
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	proc, err := c.Exec(context.Background(), "ignored-by-test-server")
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	out, _ := io.ReadAll(proc.Out)
	return string(out), proc.Wait()
}

func serveReply(ln net.Listener, cfg *ssh.ServerConfig, r reply) {
	conn, err := ln.Accept()
	if err != nil {
		return
	}
	sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer sconn.Close()
	go ssh.DiscardRequests(reqs)

	for nc := range chans {
		if nc.ChannelType() != "session" {
			nc.Reject(ssh.UnknownChannelType, "unsupported")
			continue
		}
		ch, chReqs, err := nc.Accept()
		if err != nil {
			return
		}
		go func() {
			for req := range chReqs {
				if req.Type != "exec" {
					if req.WantReply {
						req.Reply(false, nil)
					}
					continue
				}
				if req.WantReply {
					req.Reply(true, nil)
				}
				io.WriteString(ch, r.stdout)
				io.WriteString(ch.Stderr(), r.stderr)
				ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{r.exit}))
				ch.Close()
			}
		}()
	}
}

// A failing remote command must report the reason it failed. Regression test:
// session stderr used to be routed to a throwaway buffer, so this error was
// always a bare "remote exit 1" with no explanation.
func TestExecSurfacesRemoteStderrOnFailure(t *testing.T) {
	_, err := runScript(t, reply{
		stderr: "pg_dump: error: connection to server failed: FATAL: role \"bunker\" does not exist\n",
		exit:   1,
	})
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "remote exit 1") {
		t.Errorf("error = %q, want it to mention the exit status", msg)
	}
	if !strings.Contains(msg, `role "bunker" does not exist`) {
		t.Errorf("error = %q, want it to include the remote stderr", msg)
	}
}

// The other direction: a successful command must not report an error, and its
// stdout must arrive intact.
func TestExecSucceedsAndStreamsStdout(t *testing.T) {
	out, err := runScript(t, reply{stdout: "hello-from-remote"})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if out != "hello-from-remote" {
		t.Errorf("stdout = %q, want %q", out, "hello-from-remote")
	}
}

// A command that spews stderr on success must still be treated as success --
// the detail only ever appears in error messages.
func TestExecStderrOnSuccessIsNotAnError(t *testing.T) {
	out, err := runScript(t, reply{stdout: "data", stderr: "pg_dump: warning: nothing to do\n"})
	if err != nil {
		t.Fatalf("Wait: %v (stderr on a successful command must not fail the run)", err)
	}
	if out != "data" {
		t.Errorf("stdout = %q, want %q", out, "data")
	}
}

// The stderr buffer is capped, so a runaway command cannot exhaust memory, and
// the truncation is reported rather than hidden.
func TestStderrIsCappedAndReportsTruncation(t *testing.T) {
	big := strings.Repeat("E", maxStderr+4096)
	_, err := runScript(t, reply{stderr: big, exit: 1})
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "truncated") {
		t.Errorf("error = %q, want it to mention truncation", msg)
	}
	if len(msg) > maxStderr+1024 {
		t.Errorf("error is %d bytes, want it bounded near maxStderr (%d)", len(msg), maxStderr)
	}
}

func TestLimitedBufferKeepsPrefixAndCountsDropped(t *testing.T) {
	var l limitedBuffer
	l.limit = 10
	if _, err := l.Write([]byte("0123456789ABCDEF")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !strings.HasPrefix(l.String(), "0123456789") {
		t.Errorf("String() = %q, want it to start with the first 10 bytes", l.String())
	}
	if l.dropped != 6 {
		t.Errorf("dropped = %d, want 6", l.dropped)
	}
	if !strings.Contains(l.String(), "truncated") {
		t.Errorf("String() = %q, want a truncation note", l.String())
	}
}

func generateKey() (ssh.Signer, ed25519.PrivateKey, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, nil, err
	}
	return signer, priv, nil
}

func encodePEM(blk *pem.Block) []byte { return pem.EncodeToMemory(blk) }

// writeKnownHost records the server key in the exact form the knownhosts
// package will look for, including the [host]:port form used off port 22.
func writeKnownHost(t *testing.T, path, addr string, key ssh.PublicKey) {
	t.Helper()
	line := knownhosts.Normalize(addr) + " " + key.Type() + " " + base64.StdEncoding.EncodeToString(key.Marshal())
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
}

// silentListener accepts TCP connections and never says a word, like a port
// that is open but has no SSH server behind it.
func silentListener(t *testing.T) (port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { c.Close() })
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// clientFiles writes a client key and an empty known_hosts; the handshake
// never gets far enough to check either.
func clientFiles(t *testing.T) (keyPath, knownHosts string) {
	t.Helper()
	_, priv, err := generateKey()
	if err != nil {
		t.Fatal(err)
	}
	kb, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	keyPath = filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyPath, encodePEM(kb), 0o600); err != nil {
		t.Fatal(err)
	}
	knownHosts = filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(knownHosts, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return keyPath, knownHosts
}

func TestConnectHandshakeTimeout(t *testing.T) {
	port := silentListener(t)
	keyPath, kh := clientFiles(t)

	start := time.Now()
	_, err := Connect(context.Background(), Config{
		Host: "127.0.0.1", Port: port, User: "bunker",
		KeyPath: keyPath, KnownHosts: kh, Timeout: 300 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("want handshake timeout, got nil")
	}
	if !strings.Contains(err.Error(), "no SSH handshake within 300ms") {
		t.Errorf("err = %q, want handshake timeout message", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %s, want about 300ms", d)
	}
}

func TestConnectCancelDuringHandshake(t *testing.T) {
	port := silentListener(t)
	keyPath, kh := clientFiles(t)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)

	start := time.Now()
	_, err := Connect(ctx, Config{
		Host: "127.0.0.1", Port: port, User: "bunker",
		KeyPath: keyPath, KnownHosts: kh, Timeout: 30 * time.Second,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %s after cancel at 200ms; Ctrl-C must not wait for the timeout", d)
	}
}

func TestIsTimeout(t *testing.T) {
	if !isTimeout(&net.OpError{Op: "dial", Err: os.ErrDeadlineExceeded}) {
		t.Error("dial deadline should count as a timeout")
	}
	if isTimeout(errors.New("connection refused")) {
		t.Error("plain error is not a timeout")
	}
}

// fakeHost is an SSH server whose exec handler and keepalive replies the test
// controls. Freezing it stops both, like a host that lost power mid-backup.
type fakeHost struct {
	frozen chan struct{} // closed: stop answering anything
	exec   func(ch ssh.Channel, frozen <-chan struct{})
}

func (h *fakeHost) start(t *testing.T) Config {
	t.Helper()
	hostSigner, _, err := generateKey()
	if err != nil {
		t.Fatal(err)
	}
	srv := &ssh.ServerConfig{
		PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
			return &ssh.Permissions{}, nil
		},
	}
	srv.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		sconn, chans, reqs, err := ssh.NewServerConn(conn, srv)
		if err != nil {
			return
		}
		t.Cleanup(func() { sconn.Close() })
		go func() {
			for req := range reqs {
				select {
				case <-h.frozen:
					// never reply
				default:
					if req.WantReply {
						req.Reply(false, nil) // what OpenSSH says to keepalive@openssh.com
					}
				}
			}
		}()
		for nc := range chans {
			ch, chReqs, err := nc.Accept()
			if err != nil {
				return
			}
			go func() {
				for req := range chReqs {
					if req.WantReply {
						req.Reply(req.Type == "exec", nil)
					}
					if req.Type == "exec" {
						go h.exec(ch, h.frozen)
					}
				}
			}()
		}
	}()

	keyPath, knownHosts := clientFiles(t)
	writeKnownHost(t, knownHosts, ln.Addr().String(), hostSigner.PublicKey())
	return Config{
		Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, User: "bunker",
		KeyPath: keyPath, KnownHosts: knownHosts,
	}
}

// readAll runs the command and returns the error from reading stdout, or
// from Wait if the read succeeded, and how long it took.
func readAll(t *testing.T, ctx context.Context, cfg Config) (error, time.Duration) {
	t.Helper()
	c, err := Connect(context.Background(), cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	proc, err := c.Exec(ctx, "cmd")
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	defer proc.Kill()

	start := time.Now()
	if _, err := io.Copy(io.Discard, proc.Out); err != nil {
		return err, time.Since(start)
	}
	return proc.Wait(), time.Since(start)
}

// A host that freezes mid-stream must be dropped after a few missed
// keepalives, not left blocking the read until the OS gives up.
func TestKeepAliveDropsFrozenServer(t *testing.T) {
	h := &fakeHost{frozen: make(chan struct{})}
	h.exec = func(ch ssh.Channel, frozen <-chan struct{}) {
		io.WriteString(ch, "partial data")
		close(h.frozen)
		// then nothing, ever
	}
	cfg := h.start(t)
	cfg.KeepAlive = 100 * time.Millisecond

	err, took := readAll(t, context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "server stopped responding") {
		t.Fatalf("err = %v, want keepalive failure", err)
	}
	if took > 3*time.Second {
		t.Errorf("took %s, want about 4 keepalive intervals", took)
	}
}

// A command that is silent for a long time (pg_dump on a big table) on a host
// that is alive must not be cut off: sshd still answers keepalives.
func TestKeepAliveKeepsSlowCommand(t *testing.T) {
	h := &fakeHost{frozen: make(chan struct{})}
	h.exec = func(ch ssh.Channel, _ <-chan struct{}) {
		time.Sleep(800 * time.Millisecond) // 8 keepalive intervals of silence
		io.WriteString(ch, "done")
		ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
		ch.Close()
	}
	cfg := h.start(t)
	cfg.KeepAlive = 100 * time.Millisecond

	if err, _ := readAll(t, context.Background(), cfg); err != nil {
		t.Fatalf("slow but alive command failed: %v", err)
	}
}

// Ctrl-C while data is streaming must stop the read at once and say why,
// even if the server is frozen and would never close the session itself.
func TestCancelDuringStream(t *testing.T) {
	h := &fakeHost{frozen: make(chan struct{})}
	h.exec = func(ch ssh.Channel, _ <-chan struct{}) {
		io.WriteString(ch, "partial data")
		close(h.frozen)
	}
	cfg := h.start(t)
	cfg.KeepAlive = time.Minute // keepalive must not be what ends this

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)

	err, took := readAll(t, ctx, cfg)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if took > 3*time.Second {
		t.Errorf("took %s after cancel at 200ms", took)
	}
}
