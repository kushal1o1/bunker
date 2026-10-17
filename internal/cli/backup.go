package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/kushal1o1/bunker/internal/backup"
	"github.com/kushal1o1/bunker/internal/config"
	"github.com/spf13/cobra"
)

func newBackupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "manage backups",
	}
	cmd.AddCommand(newBackupRunCmd(), newBackupVerifyCmd())
	return cmd
}

func newBackupRunCmd() *cobra.Command {
	var runAll, dryRun bool
	cmd := &cobra.Command{
		Use:   "run",
		Short: "run backup for one or all servers",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBackup(cmd, args, runAll, dryRun)
		},
	}
	cmd.Flags().BoolVar(&runAll, "all", false, "backup every configured server")
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
		targets = cfg.ServerNames()
	case len(args) == 0:
		return errors.New("missing target: supply a server name or --all")
	default:
		targets = args
	}

	for _, name := range targets {
		if _, ok := cfg.Servers[name]; !ok {
			return fmt.Errorf("unknown server %q (try: bunker server list)", name)
		}
	}

	if dryRun {
		if err := printPlan(cfg, targets); err != nil {
			return err
		}
		printHint(cmd, nextRun(targets, runAll))
		return nil
	}
	// Here we attach pipeline runner
	runner := &backup.Runner{
		Cfg:  cfg,
		Logf: func(format string, args ...any) { fmt.Printf(format+"\n", args...) },
	}

	var last *backup.Result
	for _, name := range targets {
		fmt.Printf("Backing up %s (%s)\n", name, cfg.Servers[name].Host)
		res, err := runner.Run(cmd.Context(), name)
		if err != nil {
			return err
		}
		fmt.Printf("  done: %s\n", res.Dir)
		fmt.Println()
		last = res
	}
	// One snapshot gets one hint; a multi-server run stays quiet rather than
	// printing a list.
	if len(targets) == 1 && last != nil {
		printHint(cmd, nextVerify(last.Server, last.Snapshot))
	}
	return nil
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
	fmt.Println("Storage: " + cfg.Storage.Dir)
	fmt.Println("Encryption identity: " + cfg.Encryption.Identity)
	return nil
}

func newBackupVerifyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "verify <server> <snapshot>",
		Short: "verify a snapshot's checksums, archive integrity and decryption",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVerify(cmd, args)
		},
	}
	return cmd
}

// runVerify needs no network: it reads the stored snapshot, checks each
// artifact against the manifest's size and SHA-256, then decrypts and
// decompresses every artifact to prove the archive is genuinely readable.
func runVerify(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	server, snapshot := args[0], args[1]
	verifier := &backup.Verifier{
		Cfg:  cfg,
		Logf: func(format string, args ...any) { fmt.Printf(format+"\n", args...) },
	}
	fmt.Printf("Verifying %s/%s\n", server, snapshot)

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
	fmt.Fprintf(w, "\n  verified %d/%d artifact(s) — %s %s\n", n, len(res.Artifacts), res.Snapshot, verb)
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