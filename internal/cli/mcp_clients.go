package cli

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	mcpclient "github.com/sebastienrousseau/rousseau-agent/internal/mcp/client"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
	mcpadapter "github.com/sebastienrousseau/rousseau-agent/internal/tools/mcp"
)

// startMCPClients spawns every configured MCP server, walks its
// tools/list, and registers each discovered tool with registry under
// "mcp:<name>:<tool>". Returns the started clients so the caller can
// close them on shutdown, plus a slice of the tool names that got
// registered (for logging).
//
// The startup is fail-soft per client: if a single MCP server fails to
// start or list its tools, we log a WARN and continue with the others.
// A daemon should not go down because one optional integration
// misbehaves. Callers that want strict startup can inspect the returned
// clients count vs cfg.Clients length.
func startMCPClients(ctx context.Context, cfg config.MCPConfig, registry *tools.Registry, logger *slog.Logger) ([]*mcpclient.Client, []string, error) {
	if registry == nil {
		return nil, nil, errors.New("cli/mcp_clients: nil registry")
	}
	if len(cfg.Clients) == 0 {
		return nil, nil, nil
	}

	// Iterate in name order so daemon startup logs and any tool-name
	// collisions are deterministic (map iteration order in Go is not).
	names := make([]string, 0, len(cfg.Clients))
	for n := range cfg.Clients {
		names = append(names, n)
	}
	sort.Strings(names)

	clients := make([]*mcpclient.Client, 0, len(names))
	registeredAll := make([]string, 0)

	for _, name := range names {
		spec := cfg.Clients[name]
		if spec.Command == "" {
			logger.Warn("mcp.client.skip",
				slog.String("name", name),
				slog.String("reason", "empty command"),
			)
			continue
		}

		clientCfg := mcpclient.Config{
			Name:           name,
			Command:        spec.Command,
			Args:           spec.Args,
			Env:            spec.Env,
			StartTimeout:   time.Duration(spec.StartTimeoutSeconds) * time.Second,
			RequestTimeout: time.Duration(spec.RequestTimeoutSeconds) * time.Second,
			Logger:         logger,
		}

		cl, err := mcpclient.New(ctx, clientCfg)
		if err != nil {
			logger.Warn("mcp.client.start_failed",
				slog.String("name", name),
				slog.String("command", spec.Command),
				slog.String("err", err.Error()),
			)
			continue
		}

		registered, err := mcpadapter.RegisterClient(ctx, registry, cl)
		if err != nil {
			// Partial registration: log which tools worked, close the
			// client, keep going with the next server.
			logger.Warn("mcp.client.register_partial",
				slog.String("name", name),
				slog.Int("registered", len(registered)),
				slog.String("err", err.Error()),
			)
			_ = cl.Close() //nolint:errcheck // best-effort cleanup
			continue
		}

		logger.Info("mcp.client.registered",
			slog.String("name", name),
			slog.Int("tool_count", len(registered)),
		)
		clients = append(clients, cl)
		registeredAll = append(registeredAll, registered...)
	}

	return clients, registeredAll, nil
}

// closeMCPClients closes every client in the slice, logging any error
// but never returning one — shutdown is best-effort. Safe to call with
// a nil or empty slice.
func closeMCPClients(clients []*mcpclient.Client, logger *slog.Logger) {
	for _, cl := range clients {
		if cl == nil {
			continue
		}
		if err := cl.Close(); err != nil {
			logger.Warn("mcp.client.close_failed",
				slog.String("name", cl.Name()),
				slog.String("err", err.Error()),
			)
		}
	}
}

