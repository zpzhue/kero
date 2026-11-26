# Kero

Kero is a local coding assistant using Go and an OpenAI Chat Completions-compatible API. It supports Windows, macOS and Linux.

## Build and run

Install Go 1.24 or newer, then run `go build -o kero ./cmd/kero` and launch `kero` from the workspace to inspect. The first launch asks for the API base URL, model ID, optional API key environment variable and permission mode. The key is read from the environment and is not written to the configuration file. A local endpoint without authentication does not require a key.

The configuration is stored in the operating system's user configuration directory under `kero/config.json`. Run `kero config` to change it or `kero config path` to find it. Model IDs and endpoint URLs are supplied by your API provider; Magpie's local gateway can be used for testing but is not required.

## Interaction

Enter submits a message; end a line with `\` to continue it on the next line. Use the arrow keys to browse input history, and `/exit` to exit. Kero prints a session ID at startup; run `kero --resume <id>` from the same workspace to continue. Sessions are stored as JSONL under the user configuration directory. Session logs and input history can contain source code and other sensitive information.

## Permissions

- `ask`: confirm each file edit and command.
- `workspace`: allow edits inside the workspace and confirm commands.
- `auto`: allow edits inside the workspace and execute commands without confirmation.

File tools are restricted to the workspace. Commands are not sandboxed and can access other files or the network regardless of the workspace. Choose `auto` only when you trust the model and the workspace. Configuration changes take effect on the next launch.
