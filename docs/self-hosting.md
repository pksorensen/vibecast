# Self-Hosting

vibecast connects to a server that handles WebSocket relay, viewer pages, session storage, and auth. By default it uses `agentics.dk`. You can point it at your own server instead.

## The server stack

The server is two Node processes:

| Process | Port | Role |
|---------|------|------|
| **ws-relay** | 3000 (public) | WebSocket gateway + reverse proxy to Next.js |
| **Next.js** | 3001 (internal) | SSR website, API routes, session storage |

Source: [agentic-live-www](https://github.com/pksorensen/agentic-live-www)

## Docker (recommended)

```bash
git clone https://github.com/pksorensen/agentic-live-www
cd src/apps/www-site

docker build -t agentic-live .
docker run -p 3000:3000 -v agentic-data:/app/user-data agentic-live
```

Uses `supervisord` to run both processes in one container. The `-v` flag mounts a persistent volume for session/prompt history.

## Pointing vibecast at your server

```bash
AGENTIC_SERVER=localhost:3000 vibecast
```

Or set it permanently in your shell profile:

```bash
export AGENTIC_SERVER=my-server.example.com
```

The variable is just the host (and optional port) — vibecast constructs WebSocket and HTTP URLs from it automatically.

## Viewer URLs

With a custom server, viewer URLs become:

```
https://my-server.example.com/live/<broadcastId>
```

## Environment variables (server-side)

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `3000` | ws-relay public port |
| `NEXT_PORT` | `3001` | Next.js internal port |
| `USER_DATA_DIR` | `./user-data` | Persistent storage for sessions and prompts |
| `MASK_PATTERNS` | (empty) | Comma-separated regex patterns to redact from terminal output |

## Authentication

The server uses Keycloak for OAuth. For self-hosted setups without Keycloak, you can disable auth by not configuring the OAuth environment variables — unauthenticated broadcasts will still work for viewing; only the management UI is gated.

> **Note:** Keycloak has a slow cold start (~4 minutes) on a fresh volume. This is normal — Quarkus runs a re-augmentation phase before the server becomes ready.
