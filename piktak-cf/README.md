# PIK.TAK Relay for Cloudflare

This backend runs a PIK.TAK Relay in your own Cloudflare account. It accepts HTTP and browser WebSocket traffic through a Worker and keeps each active Host session in a Durable Object.

```text
Browser → Cloudflare Access → Worker + Durable Object
                                      ↑ outbound WebSocket
                                   piktakd → localhost
```

> [!WARNING]
> **Cloudflare Access is required for every public browser Service hostname.** PIK.TAK does not currently authenticate browser users, provide mature device pairing, or encrypt traffic end to end. Do not connect a real Service until Access is configured and verified.

## One-click deployment

[![Deploy to Cloudflare Workers](https://deploy.workers.cloudflare.com/button)](https://deploy.workers.cloudflare.com/?url=https://github.com/charlzyx/piktak/tree/main/piktak-cf/worker)

The button creates the Worker and its Durable Object. It does **not** configure machine secrets, custom domains, or Cloudflare Access for you. Complete every step below before connecting DSH, an Agent, an admin panel, or another real Service.

## Required setup

### 1. Create a Machine secret

Generate a random value and store it as a Worker Secret:

```sh
openssl rand -hex 16 | npx wrangler secret put PIKTAK_CODES
```

Use the same value as `code` in the Machine config. Never put it in `wrangler.toml`, Git, screenshots, or public logs.

### 2. Separate Machine and browser hostnames

Use different hostnames for Host attachment and browser traffic:

```text
relay.example.com   Machine attachment at /host
agent.example.com   browser entry for Service "agent"
```

The Machine cannot complete an interactive Access login. Keep `/host` on its own hostname or add a narrowly scoped Access bypass rule for that path/hostname. Machine attachment is currently protected only by `PIKTAK_CODES`.

### 3. Add Cloudflare Access

For **every** browser Service hostname:

1. create a Cloudflare Access self-hosted Application;
2. add an Allow Policy for the intended users;
3. make sure the policy covers HTTP and WebSocket requests;
4. visit the hostname in a signed-out browser and confirm it redirects to Access;
5. set `PIKTAK_REQUIRE_ACCESS = "1"` in `wrangler.toml`;
6. deploy again;
7. confirm a direct browser request without the Access assertion returns `401`.

Do not leave `PIKTAK_REQUIRE_ACCESS = "0"` on a real deployment.

The Worker currently checks that Cloudflare injected `cf-access-jwt-assertion`. It does not independently verify the JWT signature, audience, or expiry.

### 4. Configure the Machine

Save as `~/.config/piktak/config.yml`:

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

The `/host?name=agent` connection and browser requests whose hostname starts with `agent.` use the same Durable Object.

## Manual deployment

Requirements:

- a Cloudflare account with Workers and Durable Objects;
- Node.js and npm;
- Wrangler authenticated with `npx wrangler login`.

```sh
cd piktak-cf/worker
npm install
npm run typecheck
openssl rand -hex 16 | npx wrangler secret put PIKTAK_CODES
npm run deploy
```

`wrangler.toml` creates:

- the Worker;
- a `PIKTAK_HOST` Durable Object binding;
- one `PiktakRelay` object per Service routing key.

## Verification checklist

Before connecting a real Service, all of these must pass:

```text
[ ] /host accepts the correct Machine code
[ ] /host rejects an incorrect Machine code
[ ] signed-out browser request redirects to Cloudflare Access
[ ] Worker browser path without an Access assertion returns 401
[ ] piktakd reports paired without printing the machine code
[ ] local status API reports PairingConfigured=true without returning the secret
```

## Protocol

The Machine opens one persistent WebSocket:

```text
GET /host?name=<service>&code=<machine-code>&v=1
Upgrade: websocket
```

The Worker sends request metadata as JSON and bodies as binary frames. `piktakd` forwards them to the local target, then sends response metadata and body frames back over the same connection. Browser WebSocket messages preserve message boundaries across the tunnel.

## Limits and security

- The Machine code is a shared static secret, not one-time device pairing.
- The code is sent in the `/host` query string and may be visible in Cloudflare request metadata. This will change with the pairing protocol.
- Traffic passes through Cloudflare and is not end-to-end encrypted.
- Each Service name maps to one live Host session. A new Host replaces the previous session.
- Response buffering is in Durable Object memory and has size and timeout limits.
- Expose only required Services and review their Host, Origin, cookie, and loopback trust behavior.
- The default `workers.dev` hostname is only for initial checks. Do not use it as an unprotected browser entry point.
