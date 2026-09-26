package backup_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/klauspost/compress/zstd"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/kushal1o1/bunker/internal/backup"
	"github.com/kushal1o1/bunker/internal/config"
)

// --- fake ssh server ------------------------------------------------------

type fakeServer struct {
	addr      string
	responder func(cmd string) ([]byte, int)
	serverKey ssh.Signer
}

func startFakeServer(t *testing.T, responder func(cmd string) ([]byte, int)) *fakeServer {
	t.Helper()

	_, serverPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate server key: %v", err)
	}
	serverKey, err := ssh.NewSignerFromKey(serverPriv)
	if err != nil {
		t.Fatalf("ssh signer: %v", err)
	}

	serverCfg := &ssh.ServerConfig{
		PublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			return nil, nil
		},
	}
	serverCfg.AddHostKey(serverKey)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	fs := &fakeServer{addr: ln.Addr().String(), responder: responder, serverKey: serverKey}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go fs.handle(conn, serverCfg)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return fs
}

func (fs *fakeServer) handle(conn net.Conn, cfg *ssh.ServerConfig) {
	sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		conn.Close()
		return
	}
	defer sconn.Close()

	go ssh.DiscardRequests(reqs)

	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			newCh.Reject(ssh.UnknownChannelType, "unsupported channel type")
			continue
		}
		ch, requests, err := newCh.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer ch.Close()
			for req := range requests {
				if req.Type != "exec" {
					req.Reply(true, nil)
					continue
				}
				var payload struct{ Cmd string }
				if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
					req.Reply(false, nil)
					continue
				}
				req.Reply(true, nil)
				out, code := fs.responder(payload.Cmd)
				ch.Write(out)
				ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(code)}))
				return
			}
		}()
	}
}

// writeAuthKeyFiles writes the client private key and the server's known_hosts
// entry into temp files so the real ssh key/host verification path is used.
func writeAuthKeyFiles(t *testing.T, fs *fakeServer) (keyPath, khPath string) {
	t.Helper()

	_, clientPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate client key: %v", err)
	}
	privPem, err := ssh.MarshalPrivateKey(clientPriv, "test client key")
	if err != nil {
		t.Fatalf("marshal client key: %v", err)
	}
	keyPath = filepath.Join(t.TempDir(), "client_key")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(privPem), 0o600); err != nil {
		t.Fatalf("write client key: %v", err)
	}

	khPath = filepath.Join(t.TempDir(), "known_hosts")
	line := knownhosts.Line([]string{fs.addr}, fs.serverKey.PublicKey())
	if err := os.WriteFile(khPath, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
	return keyPath, khPath
}

// --- config / key helpers -------------------------------------------------

func writeIdentity(t *testing.T) string {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("generate age identity: %v", err)
	}
	p := filepath.Join(t.TempDir(), "key.txt")
	if err := os.WriteFile(p, []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatalf("write identity: %v", err)
	}
	return p
}

func writeConfig(t *testing.T, fs *fakeServer, keyPath, khPath, identity, storageDir, extra string) string {
	t.Helper()
	_, port, err := net.SplitHostPort(fs.addr)
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}

	cfg := fmt.Sprintf(`
servers:
  vps-01:
    host: 127.0.0.1
    ssh:
      user: backup-svc
      port: %s
      key: %s
      known_hosts: %s
    backup:
      postgres:
        dbs: [project1_db]
      paths:
        - /var/www/project1/uploads
%s
storage:
  type: local
  dir: %s

encryption:
  identity: %s
`, port, keyPath, khPath, extra, storageDir, identity)

	cfgPath := filepath.Join(t.TempDir(), "bunker.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}

func loadTestConfig(t *testing.T, path string) *config.Config {
	t.Helper()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return cfg
}

func runnerFor(cfg *config.Config) *backup.Runner {
	return &backup.Runner{Cfg: cfg, Logf: func(string, ...any) {}}
}

func decryptDecompress(t *testing.T, artifactPath, identityPath string) []byte {
	t.Helper()
	f, err := os.Open(artifactPath)
	if err != nil {
		t.Fatalf("open artifact: %v", err)
	}
	defer f.Close()

	data, err := os.ReadFile(identityPath)
	if err != nil {
		t.Fatalf("read identity: %v", err)
	}
	ids, err := age.ParseIdentities(bytes.NewReader(data))
	if err != nil || len(ids) == 0 {
		t.Fatalf("parse identity: %v", err)
	}

	r, err := age.Decrypt(f, ids[0])
	if err != nil {
		t.Fatalf("age decrypt: %v", err)
	}
	dec, err := zstd.NewReader(r)
	if err != nil {
		t.Fatalf("zstd reader: %v", err)
	}
	defer dec.Close()
	out, err := io.ReadAll(dec)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	return out
}

func hashFile(t *testing.T, path string) []byte {
	t.Helper()
	h := sha256.New()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatalf("hash %s: %v", path, err)
	}
	return h.Sum(nil)
}

// --- tests ----------------------------------------------------------------

func TestPipelinePullsVerifiesAndEncryptsSnapshot(t *testing.T) {
	fs := startFakeServer(t, func(cmd string) ([]byte, int) {
		switch {
		case strings.Contains(cmd, "pg_dump"):
			return []byte("FAKEDUMP-" + strings.Repeat("data", 10000) + "-END"), 0
		case strings.Contains(cmd, "tar "):
			return []byte("FAKETAR" + strings.Repeat("x", 100000)), 0
		default:
			return []byte("unexpected command: " + cmd), 1
		}
	})

	keyPath, khPath := writeAuthKeyFiles(t, fs)
	identity := writeIdentity(t)
	storageDir := filepath.Join(t.TempDir(), "backups")
	cfgPath := writeConfig(t, fs, keyPath, khPath, identity, storageDir, "")

	cfg := loadTestConfig(t, cfgPath)
	res, err := runnerFor(cfg).Run(context.Background(), "vps-01")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Snapshot == "" {
		t.Fatal("empty snapshot id")
	}
	if len(res.Artifacts) != 2 {
		t.Fatalf("artifacts = %d, want 2", len(res.Artifacts))
	}

	for _, art := range res.Artifacts {
		if art.SHA256 == "" || art.Size == 0 {
			t.Fatalf("artifact %s: missing size/sha256 (%+v)", art.ID, art)
		}

		full := filepath.Join(res.Dir, art.File)
		if got := hex.EncodeToString(hashFile(t, full)); got != art.SHA256 {
			t.Errorf("artifact %s: manifest sha256 %s != file sha256 %s", art.ID, art.SHA256, got)
		}

		plain := decryptDecompress(t, full, identity)
		switch {
		case art.ID == "postgres/project1_db":
			if !bytes.HasPrefix(plain, []byte("FAKEDUMP-")) || len(plain) == 0 {
				t.Errorf("pg artifact content wrong: %d bytes", len(plain))
			}
		case strings.HasPrefix(art.ID, "files/"):
			if !bytes.HasPrefix(plain, []byte("FAKETAR")) || len(plain) == 0 {
				t.Errorf("files artifact content wrong: %d bytes", len(plain))
			}
		default:
			t.Errorf("unexpected artifact id %q", art.ID)
		}
	}
}

func TestRunCancelledRemovesPartialSnapshot(t *testing.T) {
	fs := startFakeServer(t, func(cmd string) ([]byte, int) {
		if strings.Contains(cmd, "pg_dump") {
			var buf bytes.Buffer
			chunk := []byte(strings.Repeat("y", 4096))
			for i := 0; i < 64; i++ {
				buf.Write(chunk)
				time.Sleep(20 * time.Millisecond)
			}
			return buf.Bytes(), 0
		}
		return nil, 0
	})

	keyPath, khPath := writeAuthKeyFiles(t, fs)
	identity := writeIdentity(t)
	storageDir := filepath.Join(t.TempDir(), "backups")
	cfgPath := writeConfig(t, fs, keyPath, khPath, identity, storageDir, "")

	cfg := loadTestConfig(t, cfgPath)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(150*time.Millisecond, cancel)

	_, err := runnerFor(cfg).Run(ctx, "vps-01")
	if err == nil {
		t.Fatal("expected error from cancelled run, got nil")
	}

	entries, _ := os.ReadDir(storageDir)
	if len(entries) != 0 {
		t.Errorf("expected no leftover snapshots, found %d", len(entries))
	}
}