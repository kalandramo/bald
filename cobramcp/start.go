package cobramcp

import (
	"github.com/spf13/cobra"
)

// startCommandFlags holds flags for the start command.
type startCommandFlags struct {
	logLevel string
}

// startCommand creates the 'mcp start' command.
func startCommand(config *Config) *cobra.Command {
	f := &startCommandFlags{}
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the MCP server",
		Long:  `Start stdio server to expose CLI commands to AI assistants`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if config == nil {
				config = &Config{}
			}

			// stdio 模式下 stdout 是 MCP 协议通道，日志必须走 stderr。
			installStderrLogger(parseLogLevel(f.logLevel))

			// Create and start the server
			return config.serveStdio(cmd)
		},
	}

	// Add flags
	flags := cmd.Flags()
	flags.StringVar(&f.logLevel, "log-level", "", "Log level (debug, info, warn, error)")
	return cmd
}
