# MCP Server Configuration

seek can connect to external tools via the [Model Context Protocol](https://modelcontextprotocol.io/) (MCP). Each MCP server you configure is started as a subprocess and its tools are merged into seek's tool set.

## Config file location

| Platform | Path |
|---|---|
| Linux / macOS | `~/.seek/mcp.json` |
| Windows | `~/.seek/mcp.json`（即 `C:\Users\<user>\.seek\mcp.json`）|

The format is compatible with Claude Code and Cursor, so you can reuse an existing `mcp.json` unchanged.

## Format

```json
{
  "mcpServers": {
    "server-name": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
      "env": {
        "MY_VAR": "value"
      }
    }
  }
}
```

- **`command`** — the executable to launch.
- **`args`** — command-line arguments (optional).
- **`env`** — extra environment variables passed to the subprocess (optional).

## Name collisions

If an MCP tool has the same name as one of seek's built-in tools, the built-in wins. MCP tools that collide are skipped with a warning on stderr.

If two MCP servers export a tool with the same name, the one loaded last wins (server order follows the key order in `mcp.json`).

## Tool discovery

MCP tools are merged into seek's standard tool set at startup, listed alongside built-in tools when the agent decides which to call. Unlike built-in tools, MCP tools have **no system-prompt-level guidance** — the agent discovers them by their name + description alone.

> **Tip**: Give your MCP server tools descriptive names and descriptions so the agent can tell when to use them (e.g. `semble_search` vs `grep`).

## Prompt integration (design phase)

The current MCP integration is a thin bridge: tools are available but the agent receives no special instruction about when to prefer an MCP tool over a built-in one. A planned enhancement ([`docs/prd/feature-mcp-client.md`](./prd/feature-mcp-client.md)) will add:

- Prompt-layer guidance telling the agent which tool categories are available via MCP
- Query-type routing (symbol lookup → `grep`; NL question → MCP semantic search)
- Parallel dispatch for mixed queries

## Startup behaviour

seek connects to all configured servers at startup. A server that fails to start or initialize is reported on stderr but does not prevent seek from running — the remaining servers and all built-in tools are still available.

## Example: filesystem server

```json
{
  "mcpServers": {
    "fs": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/Users/you/projects"]
    }
  }
}
```

Once configured, the filesystem server's tools (e.g. `read_file`, `list_directory`) appear alongside seek's built-in tools.

## Debugging

Run seek with a prompt to see what loaded:

```
seek -p "list available tools"
```

MCP load errors appear on stderr before the first output line.
