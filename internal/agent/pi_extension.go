package agent

import _ "embed"

//go:embed pi_extension.ts
var piExtensionTS []byte

// PiExtensionTS returns the vibecast pi extension source (vibecast.ts) — the hook bridge that pi
// auto-loads from $PI_CODING_AGENT_DIR/extensions/vibecast.ts. It subscribes to pi's lifecycle
// events and execs `vibecast hook <sub>` with Claude-shaped payloads, reusing vibecast's
// agent-agnostic hook handlers (the pi analog of CodexHooksJSON). Shipped embedded so the binary
// carries it with no external asset to locate; the launch/config-seed path writes it out and the
// extension reads process.env.VIBECAST_BIN to find the vibecast binary at runtime.
func PiExtensionTS() []byte { return piExtensionTS }

// PiExtensionFileName is the filename the extension is written as inside the pi extensions dir.
const PiExtensionFileName = "vibecast.ts"
