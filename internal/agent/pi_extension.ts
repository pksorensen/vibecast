// vibecast.ts — the pi (pi-coding-agent) adapter's hook bridge.
//
// pi ships no shell-command hook system and no MCP; its extension system (in-process TS loaded
// from $PI_CODING_AGENT_DIR/extensions/) IS the hook mechanism. This extension is the pi analog
// of claude's hooks bundle and codex's hooks.json: it subscribes to pi's lifecycle events and,
// for each, execs `vibecast hook <sub>` with a Claude-Code-shaped JSON payload on stdin — reusing
// vibecast's existing, agent-agnostic hook handlers (session identity, prompt/tool metadata,
// dangerous-command guard, stop) unchanged.
//
// Shipped embedded in the vibecast binary (go:embed, see pi_extension.go) and written verbatim to
// $PI_CODING_AGENT_DIR/extensions/vibecast.ts by the launch/config-seed path, where pi auto-loads
// it. The vibecast binary path comes from process.env.VIBECAST_BIN (falls back to "vibecast" on
// PATH); cwd/VIBECAST_HOME are inherited from the pane env so `vibecast hook` finds the session.
//
// Untyped on purpose: this single file is loaded by whatever pi version/scope vibecast launches
// (@mariozechner 0.73.x today, @earendil-works later), so it avoids importing scope-specific types.

import { spawnSync } from "node:child_process";

export default function (pi: any) {
	const bin = process.env.VIBECAST_BIN || "vibecast";
	const cwd = process.cwd();

	// runHook execs `vibecast hook <sub>` with a Claude-shaped payload on stdin. Synchronous so
	// the guard's block decision is available before the tool runs. Best-effort: a spawn failure
	// never throws into pi (a broken hook must not crash the agent). Returns the child result so
	// the guard can inspect status/stdout.
	function runHook(sub: string, extra: Record<string, any>) {
		const payload = JSON.stringify({ cwd, ...extra });
		try {
			return spawnSync(bin, ["hook", sub], { input: payload, encoding: "utf8", timeout: 15000 });
		} catch {
			return null;
		}
	}

	function sessionId(ctx: any): string {
		try {
			return ctx?.sessionManager?.getSessionId?.() || "";
		} catch {
			return "";
		}
	}
	function sessionFile(ctx: any): string {
		try {
			return ctx?.sessionManager?.getSessionFile?.() || "";
		} catch {
			return "";
		}
	}

	// session_start (reason: startup|resume|new|reload|fork) → `hook session`. This is the
	// discover-identity path: session_id is pi's own UUIDv7, which the handler records back into
	// the vibecast session file. Fires at launch AND on resume — the C01/C02 registration signal.
	pi.on("session_start", (event: any, ctx: any) => {
		runHook("session", {
			hook_event_name: "SessionStart",
			session_id: sessionId(ctx),
			transcript_path: sessionFile(ctx),
			source: event?.reason || "startup",
		});
	});

	// before_agent_start carries the user's submitted prompt (typed, argv-injected, or RPC) →
	// `hook prompt` (UserPromptSubmit). The C03/C11 prompt-surfaced signal.
	pi.on("before_agent_start", (event: any, ctx: any) => {
		runHook("prompt", {
			hook_event_name: "UserPromptSubmit",
			session_id: sessionId(ctx),
			prompt: event?.prompt || "",
		});
	});

	// tool_call is the pre-execution, blockable event (input is mutable). Run the guard first;
	// if it denies (exit code 2), block the tool and feed the reason back to the model (pi shows
	// it as an isError toolResult). Otherwise emit the tool-use metadata and allow. Mirrors codex's
	// PreToolUse [guard, tool] pair. The C05 tool_use + C08 guard-deny signals.
	pi.on("tool_call", (event: any, ctx: any) => {
		const base = {
			session_id: sessionId(ctx),
			tool_name: event?.toolName || "",
			tool_input: event?.input || {},
			tool_use_id: event?.toolCallId || "",
		};
		const guard = runHook("guard", { hook_event_name: "PreToolUse", ...base });
		if (guard && guard.status === 2) {
			let reason = "vibecast guard: blocked";
			try {
				const out = JSON.parse(guard.stdout || "{}");
				reason = out?.hookSpecificOutput?.permissionDecisionReason || out?.reason || reason;
			} catch {
				/* keep default reason */
			}
			return { block: true, reason };
		}
		runHook("tool", { hook_event_name: "PreToolUse", ...base });
		return undefined;
	});

	// tool_execution_end fires after a tool runs → `hook post-tool` (PostToolUse, carries the
	// tool_response). tool_execution_start carries the id/name/args; end carries the result, so we
	// correlate via a small stack (source-order start, completion-order end — fine for the single
	// and sequential tool calls the conformance suite exercises). C05's tool_use_end.
	const pending: Array<{ tool_name: string; tool_input: any; tool_use_id: string }> = [];
	pi.on("tool_execution_start", (event: any) => {
		pending.push({
			tool_name: event?.toolName || "",
			tool_input: event?.args || {},
			tool_use_id: event?.toolCallId || "",
		});
	});
	pi.on("tool_execution_end", (event: any, ctx: any) => {
		const t = pending.pop() || { tool_name: "", tool_input: {}, tool_use_id: "" };
		let resp = "";
		try {
			const r = event?.result;
			resp = typeof r === "string" ? r : JSON.stringify(r ?? "");
		} catch {
			resp = "";
		}
		runHook("post-tool", {
			hook_event_name: "PostToolUse",
			session_id: sessionId(ctx),
			tool_name: t.tool_name,
			tool_input: t.tool_input,
			tool_use_id: t.tool_use_id,
			tool_response: resp,
		});
	});

	// agent_end fires once per completed user prompt (a turn boundary / completion signal) →
	// `hook stop` (Stop, carries the last assistant message). The C06 turn-complete signal.
	pi.on("agent_end", (event: any, ctx: any) => {
		let last = "";
		try {
			const msgs = event?.messages;
			if (Array.isArray(msgs)) {
				for (let i = msgs.length - 1; i >= 0; i--) {
					const m = msgs[i];
					if (m?.role === "assistant") {
						last = typeof m?.content === "string" ? m.content : (m?.text || "");
						break;
					}
				}
			}
		} catch {
			last = "";
		}
		runHook("stop", {
			hook_event_name: "Stop",
			session_id: sessionId(ctx),
			last_assistant_message: last,
			stop_hook_active: false,
		});
	});
}
