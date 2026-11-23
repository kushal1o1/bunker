package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/kushal1o1/bunker/internal/backup"
	"github.com/kushal1o1/bunker/internal/config"
	"github.com/spf13/cobra"
)

func newBackupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "manage backups",
	}
	cmd.AddCommand(newBackupRunCmd(), newBackupListCmd(), newBackupVerifyCmd())
	return cmd
}

func newBackupListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list [server...]",
		Short: "list stored snapshots (all servers when none given)",
		Long: `List the snapshots for each server, newest first, with their size and
status. With both storage.dir and storage.s3 set, LOCAL and S3 columns show
where each snapshot is. No SSH and no key needed. "complete" means the
manifest is valid, not that the snapshot was verified.`,
		Example: `  # every configured server
  bunker backup list

  # one or more servers
  bunker backup list my-vps web-01`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runList(cmd, args)
		},
	}
}

// runList reads storage only: no SSH and no key.
func runList(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	targets, err := targetServers(cfg, args)
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	stores, err := openStores(ctx, cfg, false)
	if err != nil {
		return err
	}

	for i, name := range targets {
		if i > 0 {
			fmt.Println()
		}
		rows, err := listMerged(ctx, stores, name)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			fmt.Printf("%s: no snapshots\n", name)
			continue
		}

		var total int64
		for _, r := range rows {
			total += r.size()
		}
		fmt.Printf("%s (%d snapshot%s, %s)\n", name, len(rows), plural(len(rows)), backup.HumanBytes(total))

		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		if stores.Both() {
			fmt.Fprintln(w, "  SNAPSHOT\tCREATED (UTC)\tARTIFACTS\tSIZE\tLOCAL\tS3")
		} else {
			fmt.Fprintln(w, "  SNAPSHOT\tCREATED (UTC)\tARTIFACTS\tSIZE\tSTATUS")
		}
		for _, r := range rows {
			created, count, size := "-", "-", "-"
			if b := r.best(); b.Status == backup.Complete {
				created = b.Created.Format("2006-01-02 15:04:05")
				count = fmt.Sprint(len(b.Manifest.Artifacts))
				size = backup.HumanBytes(b.Size())
			}
			if stores.Both() {
				fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\n", r.id, created, count, size, copyStatus(r.local), copyStatus(r.remote))
			} else {
				fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\n", r.id, created, count, size, r.best().Status)
			}
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
	return nil
}

func newBackupRunCmd() *cobra.Command {
	var runAll, dryRun bool
	cmd := &cobra.Command{
		Use:   "run [server...]",
		Short: "run backup for one or all servers",
		Long: `Take a new snapshot of each server. bunker connects over SSH, runs only
pg_dump and tar (read-only), then compresses, encrypts and writes every
artifact to local storage with a SHA-256 in manifest.json. Up to
"concurrency" servers run at once, every server is attempted even if one
fails, and the exit code is non-zero if any failed. Ctrl-C deletes partial
snapshots. --dry-run prints the plan without connecting.`,
		Example: `  # preview what would be backed up, no SSH
  bunker backup run my-vps --dry-run

  # back up one server
  bunker backup run my-vps

  # back up every configured server
  bunker backup run --all`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBackup(cmd, args, runAll, dryRun)
		},
	}
	cmd.Flags().BoolVar(&runAll, "all", false, "back up every server except those with agent: true")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the plan without backing up")
	return cmd
}

func runBackup(cmd *cobra.Command, args []string, runAll, dryRun bool) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	var targets []string
	switch {
	case runAll && len(args) > 0:
		return errors.New("cannot combine --all with specific server names")
	case runAll:
		// Agent servers back themselves up; --all means every server this
		// machine pulls.
		targets = cfg.PullServers()
		if len(targets) == 0 {
			return errors.New("no servers to pull: every server in bunker.yaml has agent: true")
		}
	case len(args) == 0:
		return errors.New("missing target: supply a server name or --all")
	default:
		targets = dedupe(args)
	}

	for _, name := range targets {
		s, ok := cfg.Servers[name]
		if !ok {
			return fmt.Errorf("unknown server %q (try: bunker server list)", name)
		}
		if s.Agent {
			return fmt.Errorf("server %q has agent: true: it backs itself up with bunker agent run on the server (see: bunker status)", name)
		}
	}

	if dryRun {
		if err := printPlan(cfg, targets); err != nil {
			return err
		}
		printHint(cmd, nextRun(targets, runAll))
		return nil
	}

	// Check the key and open storage before any SSH: a bad key, a missing
	// bucket or missing credentials should fail once, not once per server.
	if _, err := backup.ResolveRecipient(cfg.Encryption); err != nil {
		return err
	}
	stores, err := openStores(cmd.Context(), cfg, true)
	if err != nil {
		return err
	}

	// Live table on a terminal; one line per event for cron and pipes.
	var view progressView
	if isTerminal() {
		view = newLiveTable(cmd.ErrOrStderr(), targets)
	} else {
		hosts := make(map[string]string, len(targets))
		for _, name := range targets {
			hosts[name] = cfg.Servers[name].Host
		}
		view = newLineLog(cmd.OutOrStdout(), hosts)
	}

	runner := &backup.Runner{Cfg: cfg, Stores: stores, Progress: view.Event}
	start := time.Now()
	results := backup.RunMany(cmd.Context(), runner, targets, cfg.Concurrency, view.Done)
	view.Close()

	if err := printSummary(cmd.OutOrStdout(), results, time.Since(start)); err != nil {
		return err
	}

	failed, upFailed := 0, 0
	for _, r := range results {
		switch {
		case r.Err != nil:
			failed++
		case r.Result.UploadErr != nil:
			upFailed++
		}
	}
	// One snapshot gets one hint; a multi-server run stays quiet rather than
	// printing a list.
	if len(results) == 1 && failed == 0 {
		r := results[0]
		if upFailed > 0 {
			printHint(cmd, []string{"storage", "sync", r.Server})
		} else {
			printHint(cmd, nextVerify(r.Server, r.Result.Snapshot))
		}
	}
	switch {
	case failed > 0:
		return fmt.Errorf("%d of %d server(s) failed", failed, len(results))
	case upFailed > 0:
		return fmt.Errorf("%d upload(s) to s3 failed; the local snapshots are complete (retry with: bunker storage sync)", upFailed)
	}
	return nil
}

// dedupe drops repeated server names, keeping first-seen order. Two runs of
// one server in the same second would write the same snapshot directory.
func dedupe(names []string) []string {
	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

func printPlan(cfg *config.Config, targets []string) error {
	plural := "s"
	if len(targets) == 1 {
		plural = ""
	}
	fmt.Printf("Backup plan (%d server%s):\n", len(targets), plural)

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	for _, name := range targets {
		s := cfg.Servers[name]
		fmt.Fprintf(w, "  %s (%s)\n", name, s.Host)

		if s.Backup.Postgres != nil {
			for _, db := range s.Backup.Postgres.Databases {
				fmt.Fprintf(w, "    - postgres/%-30s\tpg_dump | zstd | age\n", db)
			}
		}
		for _, p := range s.Backup.Paths {
			fmt.Fprintf(w, "    - files/%-28s\ttar | zstd | age\n", strings.TrimPrefix(p, "/"))
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}

	fmt.Println()
	if cfg.Storage.Dir != "" {
		fmt.Println("Storage: " + cfg.Storage.Dir)
	}
	if s3 := cfg.Storage.S3; s3 != nil {
		fmt.Printf("Storage: s3://%s/%s (%s)\n", s3.Bucket, s3.Prefix, s3.Endpoint)
	}
	rcpt, err := backup.ResolveRecipient(cfg.Encryption)
	if err != nil {
		return err
	}
	if cfg.Encryption.Recipient != "" {
		fmt.Println("Encrypt to: " + rcpt.String() + " (encryption.recipient)")
	} else {
		fmt.Println("Encrypt to: " + rcpt.String() + " (from " + cfg.Encryption.Identity + ")")
	}
	return nil
}

func newBackupVerifyCmd() *cobra.Command {
	var remote bool
	cmd := &cobra.Command{
		Use:   "verify <server> <snapshot>",
		Short: "verify a snapshot's checksums, archive integrity and decryption",
		Long: `Check one stored snapshot without touching the network: every artifact
must exist at its recorded size, match its SHA-256, and fully decrypt and
decompress with your age identity. Exits 0 only when every check passes.
NO KEY means the snapshot was encrypted to a different key, not corruption.
Reads local storage, or S3 when it is the only store. With both, --remote
checks the S3 copy; it is streamed, never saved to disk. Servers with
agent: true are always read from S3.`,
		Example: `  # find a snapshot, then verify it
  bunker backup list my-vps
  bunker backup verify my-vps 2026-10-17T09-12-03

  # check the S3 copy instead of the local one
  bunker backup verify my-vps 2026-10-17T09-12-03 --remote`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVerify(cmd, args, remote)
		},
	}
	cmd.Flags().BoolVar(&remote, "remote", false, "verify the copy in s3 (when storage has both)")
	return cmd
}

// runVerify needs no network: it reads the stored snapshot, checks each
// artifact against the manifest's size and SHA-256, then decrypts and
// decompresses every artifact to prove the archive is genuinely readable.
func runVerify(cmd *cobra.Command, args []string, remote bool) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	stores, err := openStores(cmd.Context(), cfg, false)
	if err != nil {
		return err
	}
	server, snapshot := args[0], args[1]
	store := stores.Primary()
	// An agent's snapshots only ever exist in S3.
	if remote || cfg.Servers[server].Agent {
		if stores.Remote == nil {
			return errors.New("--remote needs storage.s3 in bunker.yaml")
		}
		store = stores.Remote
	}
	verifier := &backup.Verifier{
		Cfg:   cfg,
		Store: store,
		Logf:  func(format string, args ...any) { fmt.Printf(format+"\n", args...) },
	}
	if store.Name() == "s3" {
		fmt.Printf("Verifying %s/%s in %s\n", server, snapshot, store.Where())
	} else {
		fmt.Printf("Verifying %s/%s\n", server, snapshot)
	}

	res, err := verifier.Verify(cmd.Context(), server, snapshot)
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "  ARTIFACT\tSIZE\tSHA256\tDECRYPT")
	for _, a := range res.Artifacts {
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n", a.Entry.File, verifySize(a), verifySum(a), verifyDecrypt(a))
	}
	if res.NoKey {
		fmt.Fprintf(w, "\n  NO KEY: manifest recipient %s != local identity %s\n", res.Recipient, res.KeyID)
	}
	n := countReadable(res)
	verb := "OK"
	if !res.OK() {
		verb = "FAILED"
	}
	fmt.Fprintf(w, "\n  verified %d/%d artifact(s) - %s %s\n", n, len(res.Artifacts), res.Snapshot, verb)
	if err := w.Flush(); err != nil {
		return err
	}

	if res.OK() {
		return nil
	}
	// FAILED or NO KEY: spell out each reason so the exit code is not a
	// mystery. Size and hash mismatches pass no error around (they are
	// verdicts, not I/O failures), so they are explained from flags here.
	for _, a := range res.Artifacts {
		switch {
		case a.Err != nil:
			fmt.Printf("  FAIL %s: %v\n", a.Entry.File, a.Err)
		case !a.SizeOK:
			fmt.Printf("  FAIL %s: size mismatch\n", a.Entry.File)
		case !a.SumOK:
			fmt.Printf("  FAIL %s: sha256 mismatch\n", a.Entry.File)
		case !a.NoKey && !a.DecryptOK:
			fmt.Printf("  FAIL %s: decrypt failed\n", a.Entry.File)
		}
	}
	return fmt.Errorf("verify failed: %d/%d artifact(s) readable", n, len(res.Artifacts))
}

// verifySize renders the SIZE column: the recorded size, MISSING when the
// file is gone, FAIL when its length differs from the manifest.
func verifySize(a backup.ArtifactStatus) string {
	if a.Err != nil && errors.Is(a.Err, fs.ErrNotExist) {
		return "MISSING"
	}
	if a.SizeOK {
		return backup.HumanBytes(a.Entry.Size)
	}
	return "FAIL"
}

// verifySum renders the SHA256 column. A hash is only computed on a file
// whose size already matched the manifest; everything else gets a dash.
func verifySum(a backup.ArtifactStatus) string {
	switch {
	case a.Err != nil || !a.SizeOK:
		return "-"
	case a.SumOK:
		return "ok"
	default:
		return "FAIL"
	}
}

// verifyDecrypt renders the DECRYPT column.
func verifyDecrypt(a backup.ArtifactStatus) string {
	switch {
	case a.NoKey:
		return "NO KEY"
	case a.Err != nil:
		return "SKIP"
	case a.DecryptOK:
		return "ok"
	default:
		return "FAIL"
	}
}

// countReadable counts artifacts that passed every check, including decrypt.
// A NO KEY artifact is never readable: verification exists to prove that the
// data can be read back.
func countReadable(res *backup.VerifyResult) int {
	n := 0
	for _, a := range res.Artifacts {
		if a.SizeOK && a.SumOK && a.DecryptOK {
			n++
		}
	}
	return n
}
