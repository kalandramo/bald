package manager

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kalandramo/bald/cobramcp/internal/cfgmgr/manager/vscode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVSCodeManager(t *testing.T) {
	// Create a temporary directory for test configs
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "vscode_config.json")

	t.Run("NewVSCodeManager creates empty config", func(t *testing.T) {
		m, err := NewVSCodeManager(configPath, false)
		require.NoError(t, err)
		assert.NotNil(t, m)
		assert.Equal(t, configPath, m.configPath)
	})

	t.Run("EnableServer adds new server", func(t *testing.T) {
		m, err := NewVSCodeManager(configPath, false)
		require.NoError(t, err)

		server := vscode.Server{
			Type:    "stdio",
			Command: "/usr/local/bin/myapp",
			Args:    []string{"mcp", "start"},
		}

		err = m.EnableServer("test-server", server)
		require.NoError(t, err)

		// Verify the server was added
		assert.True(t, m.config.HasServer("test-server"))

		// Verify the config was saved
		data, err := os.ReadFile(configPath)
		require.NoError(t, err)

		var savedConfig vscode.Config
		err = json.Unmarshal(data, &savedConfig)
		require.NoError(t, err)
		assert.True(t, savedConfig.HasServer("test-server"))
	})

	t.Run("EnableServer with environment variables", func(t *testing.T) {
		m, err := NewVSCodeManager(configPath, false)
		require.NoError(t, err)

		server := vscode.Server{
			Type:    "stdio",
			Command: "/usr/local/bin/myapp",
			Args:    []string{"mcp", "start"},
			Env: map[string]string{
				"DEBUG": "true",
				"PORT":  "8080",
			},
		}

		err = m.EnableServer("env-test", server)
		require.NoError(t, err)

		// Reload and verify
		m2, err := NewVSCodeManager(configPath, false)
		require.NoError(t, err)
		assert.True(t, m2.config.HasServer("env-test"))
	})

	t.Run("DisableServer removes existing server", func(t *testing.T) {
		m, err := NewVSCodeManager(configPath, false)
		require.NoError(t, err)

		server := vscode.Server{
			Type:    "stdio",
			Command: "/usr/local/bin/myapp",
			Args:    []string{"mcp", "start"},
		}

		err = m.EnableServer("test-server", server)
		require.NoError(t, err)

		err = m.DisableServer("test-server")
		require.NoError(t, err)

		// Verify the server was removed
		assert.False(t, m.config.HasServer("test-server"))
	})

	t.Run("Multiple servers can coexist", func(t *testing.T) {
		m, err := NewVSCodeManager(configPath, false)
		require.NoError(t, err)

		server1 := vscode.Server{
			Type:    "stdio",
			Command: "/usr/local/bin/app1",
			Args:    []string{"mcp", "start"},
		}

		server2 := vscode.Server{
			Type:    "stdio",
			Command: "/usr/local/bin/app2",
			Args:    []string{"mcp", "start"},
		}

		err = m.EnableServer("server1", server1)
		require.NoError(t, err)

		err = m.EnableServer("server2", server2)
		require.NoError(t, err)

		assert.True(t, m.config.HasServer("server1"))
		assert.True(t, m.config.HasServer("server2"))

		// Disable one and verify the other remains
		err = m.DisableServer("server1")
		require.NoError(t, err)

		assert.False(t, m.config.HasServer("server1"))
		assert.True(t, m.config.HasServer("server2"))
	})
}
