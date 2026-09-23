package cli

import (
	"fmt"

	"github.com/kushal1o1/bunker/internal/backup"
	"github.com/kushal1o1/bunker/internal/pathexp"
	"github.com/spf13/cobra"
)

func newKeygenCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "keygen",
		Short: "generate an age encryption identity",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			path := pathexp.Expand(cfg.Encryption.Identity)
			if force {
				return forceGenerate(path)
			}
			pub, err := backup.GenerateIdentity(path)
			if err != nil {
				return err
			}
			fmt.Printf("generated age identity at %s\n", path)
			fmt.Printf("public key: %s\n", pub)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing identity")
	return cmd
}

func forceGenerate(path string) error {
	if pub, err := backup.FindIdentity(path); err == nil {
		if err := backup.RemoveIdentity(path); err != nil {
			return err
		}
		pubNew, err := backup.GenerateIdentity(path)
		if err != nil {
			return err
		}
		fmt.Printf("replaced age identity at %s (old %s -> %s)\n", path, pub, pubNew)
		return nil
	}
	pub, err := backup.GenerateIdentity(path)
	if err != nil {
		return err
	}
	fmt.Printf("generated age identity at %s\n", path)
	fmt.Printf("public key: %s\n", pub)
	return nil
}