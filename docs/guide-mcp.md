# MCP Server Configuration

seek can connect to external tools via the [Model Context Protocol](https://modelcontextprotocol.io/) (MCP). Each MCP server you configure is started as a subprocess and its tools are merged into seek's tool set.

## Config file location

| Platform | Path |
|---|---|
| Linux / macOS | `~/.config/seek/mcp.json` (or `$XDG_CONFIG_HOME/seek/mcp.json`) |
| Windows | `%APPDATA%\seek\mcp.json` |

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
