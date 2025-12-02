package version

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

const flagLong = "long"
const flagOutput = "output"
const outputJSON = "json"

func init() {
	Cmd.Flags().BoolP(flagLong, "l", false, "Print long version information")
	Cmd.Flags().StringP(flagOutput, "o", "text", "Output format (text|"+outputJSON+")")
}

// Cmd prints out the application's version information passed via build flags.
var Cmd = &cobra.Command{
	Use:   "version",
	Short: "Print the app version",
	RunE: func(cmd *cobra.Command, _ []string) error {
		longFormat, err := cmd.Flags().GetBool(flagLong)
		if err != nil {
			return err
		}
		outputFormat, err := cmd.Flags().GetString(flagOutput)
		if err != nil {
			return err
		}

		verInfo := NewInfo()

		var bz []byte

		if !longFormat {
			// For short format, just return version string or JSON with version only.
			switch outputFormat {
			case outputJSON:
				shortInfo := map[string]string{"version": verInfo.Version}
				bz, err = json.MarshalIndent(shortInfo, "", "  ")
			default:
				fmt.Println(verInfo.Version)
				return nil
			}
		} else {
			// For long format, return the full info.
			switch outputFormat {
			case outputJSON:
				bz, err = json.MarshalIndent(verInfo, "", "  ")
			default:
				bz, err = yaml.Marshal(&verInfo)
			}
		}

		if err != nil {
			return err
		}

		_, err = fmt.Print(string(bz))
		return err
	},
}
