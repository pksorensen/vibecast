# Changelog

## [0.1.20](https://github.com/pksorensen/vibecast/compare/v0.1.19...v0.1.20) (2026-05-02)


### Features

* add system prompt logging and cycle agent functionality ([1f1cd09](https://github.com/pksorensen/vibecast/commit/1f1cd0969dbd7e576ee7949fba06027541489d30))
* **broadcast:** show PIN + /join URL alongside LINK in TUI ([654c8fe](https://github.com/pksorensen/vibecast/commit/654c8fecc15c755165af133fae40faa01f49dc3c))
* **docs:** simplify README for end users; move technicals to docs/ ([c392d3b](https://github.com/pksorensen/vibecast/commit/c392d3bb5b20126fa81821a535918b0ebfd6e334))
* enhance hook commands and add task management hooks ([a03a07c](https://github.com/pksorensen/vibecast/commit/a03a07cd94bec248a1798d04d62670dec2913a8c))
* enhance session management and telemetry integration for Claude Code ([6470788](https://github.com/pksorensen/vibecast/commit/6470788aaf90ac8ecf02d22d8d6fd1da25b32507))
* **hooks:** subagentPromptAppendix via SubagentStart hook ([f9a7b4a](https://github.com/pksorensen/vibecast/commit/f9a7b4acb996a4db4311fb71220e2076883b5762))
* **hooks:** update hooks, mcp serve, and stream with plugin support ([d470f56](https://github.com/pksorensen/vibecast/commit/d470f5668125207a63cd40cc2ca24325a0f85811))
* released code opensource ([5d8240a](https://github.com/pksorensen/vibecast/commit/5d8240a1bddce068bc1186689b0f583c48583c84))
* **viewers:** add RunViewers command to display current viewer count ([677469c](https://github.com/pksorensen/vibecast/commit/677469c6f324fb4f0d06138392ccb161136d3a39))


### Bug Fixes

* **otel:** rename vibecast.session_id → vibecast.stream_id in OTEL_RESOURCE_ATTRIBUTES ([35d340d](https://github.com/pksorensen/vibecast/commit/35d340d9408331f64e7c07163ec43dfa7336d99a))
* **stream:** propagate AGENTICS_PROXY_* env vars into tmux session ([7fea32d](https://github.com/pksorensen/vibecast/commit/7fea32d4abf6b658f063f7ab1425d3866fc950fa))

## [0.1.19](https://github.com/pksorensen/agentic-live-www/compare/vibecast-v0.1.18...vibecast-v0.1.19) (2026-03-29)


### Features

* implement terminal snapshot handling and telemetry display in live sessions ([40f1397](https://github.com/pksorensen/agentic-live-www/commit/40f1397b73d1816b2d41f4b05a168618a69711a3))

## [0.1.18](https://github.com/pksorensen/agentic-live-www/compare/vibecast-v0.1.17...vibecast-v0.1.18) (2026-03-29)


### Features

* implement submission tracking and status reporting for vibecheck analysis ([181c339](https://github.com/pksorensen/agentic-live-www/commit/181c339432ab00d52449d820cf5b43dcb21efdd2))

## [0.1.17](https://github.com/pksorensen/agentic-live-www/compare/vibecast-v0.1.16...vibecast-v0.1.17) (2026-03-29)


### Features

* enhance hooks and session handling with job mode enforcement and commit log retrieval ([e5e399c](https://github.com/pksorensen/agentic-live-www/commit/e5e399cb82127770ee6fbb8a9d987c69ab1c7692))

## [0.1.16](https://github.com/pksorensen/agentic-live-www/compare/vibecast-v0.1.15...vibecast-v0.1.16) (2026-03-29)


### Features

* add artifact management and repository file browsing features ([3196b9e](https://github.com/pksorensen/agentic-live-www/commit/3196b9eba3d9fbfdcb91b491a81b3f093628b775))

## [0.1.15](https://github.com/pksorensen/agentic-live-www/compare/vibecast-v0.1.14...vibecast-v0.1.15) (2026-03-29)


### Features

* update build script to ensure claude-plugin is copied next to vibecast binaries ([ffd9b1a](https://github.com/pksorensen/agentic-live-www/commit/ffd9b1a386570a1387c28f52c85e2f5297105779))

## [0.1.14](https://github.com/pksorensen/agentic-live-www/compare/vibecast-v0.1.13...vibecast-v0.1.14) (2026-03-28)


### Features

* add dev tunnel support and job requeue functionality ([76766da](https://github.com/pksorensen/agentic-live-www/commit/76766da5743277e9438546a9ef29bbdcb6c89623))
* enhance URL detection and handling in broadcast and metadata APIs ([14f1443](https://github.com/pksorensen/agentic-live-www/commit/14f1443e3109cf4ad131e0e3e51e6615bc0826ae))

## [0.1.13](https://github.com/pksorensen/agentic-live-www/compare/vibecast-v0.1.12...vibecast-v0.1.13) (2026-03-26)


### Features

* implement stage repository management and automation ([98dec3c](https://github.com/pksorensen/agentic-live-www/commit/98dec3c5c2d80b80cd1957e903bfa0ed4a0a32e0))

## [0.1.12](https://github.com/pksorensen/agentic-live-www/compare/vibecast-v0.1.11...vibecast-v0.1.12) (2026-03-15)


### Features

* Add functions for reading and appending OTEL events in the store ([8f2fecb](https://github.com/pksorensen/agentic-live-www/commit/8f2fecb31bdcd6d8385451027163c41a76c4ff8b))
* Add idle and max timeout properties to Agent Definition in agentics-store ([8f2fecb](https://github.com/pksorensen/agentic-live-www/commit/8f2fecb31bdcd6d8385451027163c41a76c4ff8b))
* Create Tic-Tac-Toe game logic with state management and move validation ([8f2fecb](https://github.com/pksorensen/agentic-live-www/commit/8f2fecb31bdcd6d8385451027163c41a76c4ff8b))
* Define types for Agent Race game including resources, players, and game state ([8f2fecb](https://github.com/pksorensen/agentic-live-www/commit/8f2fecb31bdcd6d8385451027163c41a76c4ff8b))
* Extend WebSocket relay to handle project events and game state updates ([8f2fecb](https://github.com/pksorensen/agentic-live-www/commit/8f2fecb31bdcd6d8385451027163c41a76c4ff8b))
* Implement Agent Race game engine with game state management, player actions, and resource handling ([8f2fecb](https://github.com/pksorensen/agentic-live-www/commit/8f2fecb31bdcd6d8385451027163c41a76c4ff8b))
* Implement OTEL aggregation for processing and summarizing telemetry data ([8f2fecb](https://github.com/pksorensen/agentic-live-www/commit/8f2fecb31bdcd6d8385451027163c41a76c4ff8b))
* Introduce project events system with WebSocket support for real-time updates ([8f2fecb](https://github.com/pksorensen/agentic-live-www/commit/8f2fecb31bdcd6d8385451027163c41a76c4ff8b))


### Bug Fixes

* Enhance masking utility to include runner tokens ([8f2fecb](https://github.com/pksorensen/agentic-live-www/commit/8f2fecb31bdcd6d8385451027163c41a76c4ff8b))
* Update TypeScript configuration to include new game module ([8f2fecb](https://github.com/pksorensen/agentic-live-www/commit/8f2fecb31bdcd6d8385451027163c41a76c4ff8b))

## [0.1.11](https://github.com/pksorensen/agentic-live-www/compare/vibecast-v0.1.10...vibecast-v0.1.11) (2026-02-28)


### Features

* add "sync" command and implement sync functionality for session data ([c89beae](https://github.com/pksorensen/agentic-live-www/commit/c89beaee651a59b2f1b00014c9e3fcfbddf59b72))
* add SettingsPanel component for stream session management ([ec5a1cc](https://github.com/pksorensen/agentic-live-www/commit/ec5a1cc7e168a4a2254a7bda604d25cc5a61d078))

## [0.1.10](https://github.com/pksorensen/agentic-live-www/compare/vibecast-v0.1.9...vibecast-v0.1.10) (2026-02-27)


### Features

* add live session management and voting system ([0d1c1f8](https://github.com/pksorensen/agentic-live-www/commit/0d1c1f82aba5ce50b354091ca8a061682f19cef8))

## [0.1.9](https://github.com/pksorensen/agentic-live-www/compare/vibecast-v0.1.8...vibecast-v0.1.9) (2026-02-26)


### Features

* add JoinPage component for entering live session invite codes ([717a6c4](https://github.com/pksorensen/agentic-live-www/commit/717a6c403b589faa1389306cba9cce72e9169da8))

## [0.1.8](https://github.com/pksorensen/agentic-live-www/compare/vibecast-v0.1.7...vibecast-v0.1.8) (2026-02-26)


### Bug Fixes

* update release workflow and improve package description for vibecast ([e6568de](https://github.com/pksorensen/agentic-live-www/commit/e6568de5649593303ab93ff4ef64b7a9153b62fa))

## [0.1.7](https://github.com/pksorensen/agentic-live-www/compare/vibecast-v0.1.6...vibecast-v0.1.7) (2026-02-26)


### Bug Fixes

* update release workflow to improve PR handling and add repository info to package.json files ([bcab192](https://github.com/pksorensen/agentic-live-www/commit/bcab1920781579fcbbe3f76e05559c88cb06255c))

## [0.1.6](https://github.com/pksorensen/agentic-live-www/compare/vibecast-v0.1.5...vibecast-v0.1.6) (2026-02-26)


### Bug Fixes

* add id-token permission for publish-cli job and enable provenance for npm publish ([3af1311](https://github.com/pksorensen/agentic-live-www/commit/3af131154a7dd1f56d162ad669302c3524be0013))

## [0.1.5](https://github.com/pksorensen/agentic-live-www/compare/vibecast-v0.1.4...vibecast-v0.1.5) (2026-02-26)


### Features

* Implement windowing components and social media integration ([3b3539e](https://github.com/pksorensen/agentic-live-www/commit/3b3539e8dc4f69944b82cedefb2de9179ce811f0))

## [0.1.4](https://github.com/pksorensen/agentic-live-www/compare/vibecast-v0.1.3...vibecast-v0.1.4) (2026-02-16)


### Features

* **auth:** integrate Keycloak authentication with NextAuth and JWT validation ([cd242c5](https://github.com/pksorensen/agentic-live-www/commit/cd242c52932d50959e94af8d49c6be8d3dced823))
* **masking:** implement sensitive data masking for terminal output ([cd242c5](https://github.com/pksorensen/agentic-live-www/commit/cd242c52932d50959e94af8d49c6be8d3dced823))
* **store:** create user data store with session and project management ([cd242c5](https://github.com/pksorensen/agentic-live-www/commit/cd242c52932d50959e94af8d49c6be8d3dced823))


### Bug Fixes

* **ws-relay:** enhance WebSocket relay with authentication and state restoration ([cd242c5](https://github.com/pksorensen/agentic-live-www/commit/cd242c52932d50959e94af8d49c6be8d3dced823))

## [0.1.3](https://github.com/pksorensen/agentic-live-www/compare/vibecast-v0.1.2...vibecast-v0.1.3) (2026-02-15)


### Features

* **cli:** add transition animation and update UI feedback during stream operations ([cbe4623](https://github.com/pksorensen/agentic-live-www/commit/cbe4623d5e3b3603b677584a62692d994ff4b1f0))

## [0.1.2](https://github.com/pksorensen/agentic-live-www/compare/vibecast-v0.1.1...vibecast-v0.1.2) (2026-02-15)


### Features

* **cli:** add CRT retro terminal frame UI and fix website build ([50798ca](https://github.com/pksorensen/agentic-live-www/commit/50798cae6d820f7aed04ca3b91e5f5c30d5b50b1))

## [0.1.1](https://github.com/pksorensen/agentic-live-www/compare/vibecast-v0.1.0...vibecast-v0.1.1) (2026-02-14)


### Features

* add health check API route for lives service ([99376fe](https://github.com/pksorensen/agentic-live-www/commit/99376fe041380fb379e9370a28c6ab5a57fe939d))
* add wait-for-text script for tmux pane monitoring ([99376fe](https://github.com/pksorensen/agentic-live-www/commit/99376fe041380fb379e9370a28c6ab5a57fe939d))
* create server.mjs for handling WebSocket connections and broadcasting ([99376fe](https://github.com/pksorensen/agentic-live-www/commit/99376fe041380fb379e9370a28c6ab5a57fe939d))
* implement live stream page with terminal and chat functionality ([99376fe](https://github.com/pksorensen/agentic-live-www/commit/99376fe041380fb379e9370a28c6ab5a57fe939d))
* implement terminal output masking in masking.mjs ([99376fe](https://github.com/pksorensen/agentic-live-www/commit/99376fe041380fb379e9370a28c6ab5a57fe939d))
* update Dockerfile to use custom server and install ws dependency ([99376fe](https://github.com/pksorensen/agentic-live-www/commit/99376fe041380fb379e9370a28c6ab5a57fe939d))
