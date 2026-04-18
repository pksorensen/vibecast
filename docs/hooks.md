# Claude Code Hooks

vibecast ships a Claude Code plugin in `claude-plugin/` that wires up hooks so viewers see live status — not just raw terminal output.

## How hooks are loaded

When vibecast starts a broadcast, it passes `--plugin-dir claude-plugin/` to Claude Code. Claude reads `claude-plugin/hooks/hooks.json` and registers the hook commands.

Each hook calls `vibecast hook <event>` with the event payload on stdin.

## Hook events

| Claude Code event | vibecast hook | What it publishes |
|-------------------|---------------|-------------------|
| `UserPromptSubmit` | `vibecast hook prompt` | Prompt text (masked), token count estimate |
| `SessionStart` | `vibecast hook session` | Session ID, project path, Claude session ID |
| `PreToolUse` | `vibecast hook tool` | Tool name, input parameters |
| `PostToolUse` | `vibecast hook tool` | Tool name, output, success/failure |
| `Stop` | `vibecast hook session` | Session end, exit reason |

## Masking

Prompt content and tool inputs are passed through the server's masking pipeline before being published to viewers. Patterns like API keys, tokens, and file paths matching `MASK_PATTERNS` are redacted.

## What viewers see

Published metadata builds the **activity log** in the viewer UI:

- Session start/end events with timestamps
- Each prompt submitted (masked)
- Every tool call with name and result status
- Plan extractions from prompt content

## Hook payload format

Hooks receive a JSON payload on stdin in Claude Code's standard hook envelope:

```json
{
  "session_id": "...",
  "hook_event_name": "PreToolUse",
  "tool_name": "Write",
  "tool_input": { "file_path": "...", "content": "..." }
}
```

vibecast extracts the relevant fields and POSTs to `/api/lives/metadata` with the session context.

## Adding custom hooks

You can extend the plugin by adding entries to `claude-plugin/hooks/hooks.json`. The built-in hooks use `async: true` (fire-and-forget) for metadata and `async: false` for `SessionStart` (blocks until the hook completes so session state is registered before Claude proceeds).
