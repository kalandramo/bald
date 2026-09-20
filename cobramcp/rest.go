package cobramcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"

	baldlog "github.com/kalandramo/bald/log"
)

// serveREST exposes every cobra command as a plain REST endpoint.
//
// # Route convention
//
//	POST /{toolName}
//
// toolName is identical to the MCP tool name produced by [toolName], e.g.
// "myapp_sre_open" or "myapp_oncall".
//
// # Request
//
// Content-Type: application/json
// Body: a flat JSON object whose keys are the parameter names exactly as they
// appear in the tool's inputSchema — flags and positional arguments at the same
// level, no nesting:
//
//	{ "namespace": "prod", "module": "bke", "title": "pod crash", "level": 3 }
//
// An empty body (or omitted Content-Type) is treated as zero parameters.
//
// # Response
//
// HTTP 200 — command executed (check exitCode for success/failure):
//
//	{ "stdout": "...", "stderr": "...", "exitCode": 0 }
//
// HTTP 400 — malformed JSON body:
//
//	{ "error": "invalid JSON body: ..." }
//
// HTTP 404 — unknown tool name:
//
//	{ "error": "tool \"x\" not found" }
//
// HTTP 405 — wrong HTTP method (only POST is accepted):
//
//	{ "error": "method GET not allowed; use POST" }
//
// HTTP 500 — subprocess could not be launched:
//
//	{ "error": "..." }
func (c *Config) serveREST(cmd *cobra.Command, addr, baseURL string) error {
	// Initialise slogger, walk the command tree, populate c.tools / c.toolMetas.
	if err := c.registerTools(cmd); err != nil {
		return err
	}

	mux := http.NewServeMux()

	// OAuth 资源元数据端点（RFC 9728）。REST 形态自建 mux，不像 SSE 由
	// mcp-go 内部按路径分发，故需在此显式挂载。该路径免认证（公开发现端点）。
	if c.OAuthProtectedResource != nil {
		mux.Handle(
			mcpserver.ProtectedResourceMetadataPath(c.OAuthProtectedResource.Resource),
			mcpserver.NewProtectedResourceMetadataHandler(*c.OAuthProtectedResource),
		)
	}

	// Register one POST handler per tool.
	for _, tool := range c.tools {
		name := tool.Name
		meta, ok := c.toolMetas[name]
		if !ok {
			meta = toolMeta{flagNames: make(map[string]struct{})}
		}

		mux.HandleFunc("/"+name, restToolHandler(name, meta))
	}

	// Catch-all: JSON 404 for any path not matched above.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Path
		if len(name) > 0 && name[0] == '/' {
			name = name[1:]
		}
		restWriteError(w, http.StatusNotFound, fmt.Sprintf("tool %q not found", name))
	})

	effectiveBaseURL := baseURL
	if effectiveBaseURL == "" {
		effectiveBaseURL = "http://" + addr
	}
	cmd.Printf("REST API server listening on %q (base URL: %s)\n", addr, effectiveBaseURL)
	baldlog.Info(cmd.Context(), "REST API server listening", "addr", addr, "baseURL", effectiveBaseURL)

	// 认证中间件包裹整个 mux（AuthMiddleware 外层 + AuthToken 内层），
	// well-known 元数据路径免认证。
	var handler http.Handler = mux
	if mw := buildAuthMiddleware(c.AuthMiddleware, c.AuthToken, c.publicAuthPaths()...); mw != nil {
		handler = mw(handler)
	}

	srv := &http.Server{
		Addr:    addr,
		Handler: handler,
	}

	// Graceful shutdown when the cobra command context is cancelled.
	// Use a bounded deadline, consistent with serveStdio/serveHTTP: an
	// unbounded context.Background() would hang the shutdown forever if a
	// handler never returns.
	go func() {
		<-cmd.Context().Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			baldlog.Error(context.Background(), "REST server shutdown error", "error", err)
		}
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("REST server error: %w", err)
	}
	return nil
}

// restToolHandler returns an http.HandlerFunc for a single tool.
// The handler decodes the flat JSON request body into a ToolInput, builds the
// cobra CLI argument list via the same splitFlatInput / buildFlagArgs pipeline
// used by the MCP subprocess model, runs the binary as a subprocess, and
// writes the ToolOutput as JSON.
func restToolHandler(toolName string, meta toolMeta) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			restWriteError(w, http.StatusMethodNotAllowed,
				fmt.Sprintf("method %s not allowed; use POST", r.Method))
			return
		}

		// Decode the flat JSON request body.
		var flat map[string]any
		dec := json.NewDecoder(r.Body)
		if err := dec.Decode(&flat); err != nil {
			// EOF means an empty body — treat as no parameters.
			if err.Error() == "EOF" {
				flat = map[string]any{}
			} else {
				restWriteError(w, http.StatusBadRequest,
					fmt.Sprintf("invalid JSON body: %v", err))
				return
			}
		}

		baldlog.Info(r.Context(), "REST tool request", "tool", toolName, "params", flat)

		// Build the ordered arg name list from the meta's argSpecs.
		argNames := make([]string, len(meta.argSpecs))
		for i, spec := range meta.argSpecs {
			argNames[i] = spec.Name
		}

		input := ToolInput{
			CmdPath:   meta.cmdPath,
			FlatInput: flat,
			FlagNames: meta.flagNames,
			ArgNames:  argNames,
		}

		// Reconstruct the cobra CLI argument slice (same path as MCP subprocess).
		args := buildCommandArgs(input)
		baldlog.Debug(r.Context(), "REST subprocess args", "tool", toolName, "args", args)

		output, err := execSubprocess(r.Context(), args)
		if err != nil {
			restWriteError(w, http.StatusInternalServerError, err.Error())
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(output)
	}
}

// restWriteError writes a JSON error response.
func restWriteError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
