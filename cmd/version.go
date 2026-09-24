package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/nuomiiiii/lite/pkg/selfupdate"
	"github.com/nuomiiiii/lite/utils"
	"github.com/spf13/cobra"
)

var (
	versionJSON         bool
	versionCapabilities bool
)

var VersionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the Lite version",
	RunE: func(command *cobra.Command, _ []string) error {
		out := command.OutOrStdout()
		// The plain and --json forms are consumed by the updater and must stay
		// byte-compatible, so the capability probe is strictly opt-in.
		if versionCapabilities {
			capability := selfupdate.DetectCapability()
			if versionJSON {
				return json.NewEncoder(out).Encode(map[string]any{
					"version":    utils.CurrentVersion,
					"hash":       utils.VersionHash,
					"capability": capability,
				})
			}
			supported := "false"
			if capability.Supported {
				supported = "true"
			}
			fmt.Fprintf(out, "%s (%s)\n", utils.CurrentVersion, utils.VersionHash)
			fmt.Fprintf(out, "deployment=%s self_update=%s", capability.Deployment, supported)
			if capability.Reason != "" {
				fmt.Fprintf(out, " reason=%s", capability.Reason)
			}
			fmt.Fprintln(out)
			return nil
		}
		if versionJSON {
			return json.NewEncoder(out).Encode(map[string]string{
				"version": utils.CurrentVersion,
				"hash":    utils.VersionHash,
			})
		}
		fmt.Fprintf(out, "%s (%s)\n", utils.CurrentVersion, utils.VersionHash)
		return nil
	},
}

func init() {
	VersionCmd.Flags().BoolVar(&versionJSON, "json", false, "print machine-readable JSON")
	VersionCmd.Flags().BoolVar(&versionCapabilities, "capabilities", false, "also print deployment and self-update capability")
	RootCmd.AddCommand(VersionCmd)
}
