# PIK.TAK Relay for Cloudflare

This backend runs the PIK.TAK Relay in your own Cloudflare account. It is designed for HTTP and browser WebSocket services used from a phone or browser.

```text
Browser → Cloudflare Access → Worker + Durable Object
                                      ↑ outbound WebSocket
                                   piktakd → localhost
```

The Worker is HTTP-aware. The Go Relay is the separate self-hosted backend for transparent TCP.

## Requirements

- a Cloudflare account with Workers and Durable Objects;
- Node.js and npm;
- Wrangler authenticated with `npx wrangler login`;
- a random Machine code;
- Cloudflare Access for every public browser hostname in a real deployment.

## Deploy

```sh
cd piktak-cf/worker
npm install
npm run typecheck
openssl rand -hex 16 | npx wrangler secret put PIKTAK_CODES
npm run deploy
```

`wrangler.toml` creates:

- the `piktak` Worker;
- a `PIKTAK_HOST` Durable Object binding;
- one `PiktakRelay` object per service routing key.

Do not commit `PIKTAK_CODES`. It is a comma-separated allowlist when more than one code is needed.

## Connect a Machine

```yaml
relay: wss://relay.example.com/host
code: replace-with-the-same-secret
status: true
status_addr: 127.0.0.1:8765

services:
  - name: agent
    protocol: ws
    local: 127.0.0.1:3080
    rewrite_host: true
```

Run:

```sh
piktakd -config ~/.config/piktak/config.yml
```

The `/host?name=agent` connection and requests whose hostname begins with `agent.` use the same Durable Object.

## Hostnames and Access

A practical setup uses separate hostnames:

```text
relay.example.com   Machine attachment at /host
agent.example.com   browser entry for Service "agent"
```

Protect every browser hostname with Cloudflare Access. The Machine attachment cannot complete an interactive Access login, so put `/host` on a hostname or Access bypass rule that relies on the PIK.TAK Machine code.

After Access is configured, change:

```toml
PIKTAK_REQUIRE_ACCESS = "1"
```

and deploy again. With this setting, browser requests without `cf-access-jwt-assertion` receive `401`.

The current implementation checks that Cloudflare injected the assertion. It does not independently verify the JWT signature, audience, or expiry inside the Worker.

## Protocol

The Machine opens one persistent WebSocket:

```text
GET /host?name=<service>&code=<machine-code>&v=1
Upgrade: websocket
```

The Worker sends request metadata as JSON and bodies as binary frames. `piktakd` forwards them to the local target, then sends response metadata and body frames back over the same connection. Browser WebSocket messages use the same tunnel with message boundaries preserved.

## Limits and security

- The Machine code is currently a shared static secret, not one-time device pairing.
- The code appears in the `/host` query string and may be visible in platform request logs. This will change with the pairing protocol.
- Traffic passes through Cloudflare and is not end-to-end encrypted.
- Each service name maps to one live Host session. A new Host replaces the previous session for that name.
- Response buffering is in Durable Object memory and has explicit size and timeout limits.
- Expose only required services and review their Host, Origin, cookie, and loopback trust behavior.
- Use a custom domain and Access before exposing a real service. The default `workers.dev` hostname is intended only for initial verification.

## Local check

```sh
mkdir -p /tmp/piktak-local
echo '<h1>PIK.TAK local OK</h1>' > /tmp/piktak-local/index.html
python3 -m http.server 7531 -d /tmp/piktak-local
```

Point `piktakd` at `127.0.0.1:7531`, then request the browser hostname after the Host reports connected.
