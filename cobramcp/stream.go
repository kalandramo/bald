package cobramcp

import (
	"fmt"

	"github.com/spf13/cobra"
)

// streamCommand holds flags for the stream command.
type streamCommandFlags struct {
	logLevel string
	host     string
	port     int
}

// startCommand creates the 'mcp start' command.
func streamCommand(config *Config) *cobra.Command {
	f := &streamCommandFlags{}
	cmd := &cobra.Command{
		Use:   "stream",
		Short: "Stream the MCP server over HTTP",
		Long:  `Start HTTP server to expose CLI commands to AI assistants`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if config == nil {
				config = &Config{}
			}

			// stdio 模式下 stdout 是 MCP 协议通道，日志必须走 stderr。
			installStderrLogger(parseLogLevel(f.logLevel))

			// Create and start the server
			return config.serveHTTP(cmd, fmt.Sprintf("%s:%d", f.host, f.port))
		},
	}

	// Add flags
	flags := cmd.Flags()
	flags.StringVar(&f.logLevel, "log-level", "", "Log level (debug, info, warn, error)")
	flags.StringVar(&f.host, "host", "", "host to listen on")
	flags.IntVar(&f.port, "port", 8080, "port number to listen on")
	return cmd
}
