package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/kushal1o1/bunker/internal/backup"
	"github.com/kushal1o1/bunker/internal/pathexp"
	"github.com/spf13/cobra"
)

func newKeygenCmd() *cobra.Command {
	var force, public bool
	cmd := &cobra.Command{
		Use:   "keygen",
		Short: "generate an age encryption identity",
		Long: `Create the age keypair that encrypts every backup, at encryption.identity
from bunker.yaml (0600). Refuses to overwrite an existing identity. --force
replaces it, and every snapshot made with the old key becomes undecryptable.
--public prints the public key of the existing identity, for
encryption.recipient.`,
		Example: `  # first-time setup
  bunker keygen

  # print the public key, for encryption.recipient on a backup machine
  bunker keygen --public

  # rotate the key (old snapshots can no longer be decrypted)
  bunker keygen --force`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			path := pathexp.Expand(cfg.Encryption.Identity)
			out := cmd.OutOrStdout()
			switch {
			case public:
				pub, err := backup.FindIdentity(path)
				if err != nil {
					return err
				}
				fmt.Fprintln(out, pub)
				return nil
			case force:
				return forceGenerate(out, path)
			}
			pub, err := backup.GenerateIdentity(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "generated age identity at %s\n", path)
			fmt.Fprintf(out, "public key: %s\n", pub)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing identity")
	cmd.Flags().BoolVar(&public, "public", false, "print the public key of the existing identity")
	cmd.MarkFlagsMutuallyExclusive("force", "public")
	return cmd
}

// forceGenerate replaces the identity at path, or creates it if there is
// none. A file that exists but cannot be parsed is replaced too: that is the
// case --force exists for.
func forceGenerate(out io.Writer, path string) error {
	_, statErr := os.Stat(path)
	existed := statErr == nil
	oldPub, oldErr := backup.FindIdentity(path)

	pub, err := backup.ReplaceIdentity(path)
	if err != nil {
		return err
	}
	if !existed {
		fmt.Fprintf(out, "generated age identity at %s\n", path)
		fmt.Fprintf(out, "public key: %s\n", pub)
		return nil
	}
	fmt.Fprintf(out, "replaced age identity at %s\n", path)
	fmt.Fprintf(out, "public key: %s\n", pub)
	if oldErr != nil {
		fmt.Fprintln(out, "old public key: unknown (the old file could not be read)")
	} else {
		fmt.Fprintf(out, "old public key: %s (snapshots made with it need the old key to verify)\n", oldPub)
	}
	return nil
}
