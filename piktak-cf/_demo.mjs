// Zero-dependency transport-check backend: HTTP + SSE + WS on one port.
// Used to validate mesh relays (tcp protocol via mesh-go ingress, ws protocol
// via a mesh-cf Worker) carry HTTP, long-lived chunked SSE, and WS event
// streams. The WS server is hand-rolled (RFC 6455 server side) so the file
// needs no npm packages.
import http from 'node:http';
import crypto from 'node:crypto';

const PORT = Number(process.env.PORT) || 7541;
const WS_MAGIC = '258EAFA5-E914-47DA-95CA-C5AB0DC85B11';

const server = http.createServer((req, res) => {
  const url = req.url ?? '/';
  if (url === '/api/ping') {
    res.writeHead(200, { 'content-type': 'application/json' });
    res.end(JSON.stringify({ ok: true, transport: 'http', t: Date.now() }));
    return;
  }
  if (url.startsWith('/sse')) {
    res.writeHead(200, {
      'content-type': 'text/event-stream',
      'cache-control': 'no-cache',
      connection: 'keep-alive',
    });
    let n = 0;
    const iv = setInterval(() => {
      n += 1;
      res.write(`data: ${JSON.stringify({ transport: 'sse', n, t: Date.now() })}\n\n`);
      if (n >= 5) { clearInterval(iv); res.end(); }
    }, 800);
    req.on('close', () => clearInterval(iv));
    return;
  }
  if (url.startsWith('/ws')) { return handleWs(req, res); }
  res.writeHead(200, { 'content-type': 'text/html' });
  res.end(`<!doctype html><meta charset=utf-8><title>mesh demo</title>
<body style="font-family:monospace"><h2>mesh transport demo</h2>
<div id=ping>HTTP /api/ping …</div><div id=sse>SSE /sse …</div><div id=ws>WS /ws …</div>
<script>
fetch('/api/ping').then(r=>r.json()).then(j=>ping.textContent='HTTP OK: '+JSON.stringify(j)).catch(e=>ping.textContent='HTTP ERR '+e);
const es=new EventSource('/sse'); let s=0; es.onmessage=m=>{s++; sse.textContent='SSE #'+s+': '+m.data;}; es.onerror=()=>{sse.textContent+=' [ended]'; es.close();};
const ws=new WebSocket((location.protocol==='https:'?'wss':'ws')+'://'+location.host+'/ws');
ws.onopen=()=>{ws.textContent='WS open'; ws.send('hello from browser');};
ws.onmessage=m=>{ws.textContent='WS recv: '+m.data;};
ws.onerror=()=>{ws.textContent='WS ERR';};
</script></body></html>`);
});

// --- minimal RFC 6455 server: handshake + single-frame text/binary echo ------

function handleWs(req, res) {
  const key = req.headers['sec-websocket-key'];
  if (!key) { res.writeHead(400).end('bad ws handshake'); return; }
  const accept = crypto.createHash('sha1').update(key + WS_MAGIC).digest('base64');
  res.writeHead(101, {
    'Upgrade': 'websocket',
    'Connection': 'Upgrade',
    'Sec-WebSocket-Accept': accept,
  });
  res.flushHeaders?.();
  try { req.socket.write(frame(0x1, Buffer.from('ws welcome from demo\n'))); } catch {}

  let buf = Buffer.alloc(0);
  res.on('error', () => {});
  req.socket.on('data', (chunk) => {
    buf = Buffer.concat([buf, chunk]);
    for (;;) {
      const f = parseFrame(buf);
      if (!f) break;
      buf = buf.subarray(f.consumed);
      if (f.opcode === 0x8) { // close
        try { req.socket.write(frame(0x8, Buffer.from([0x03, 0xe8]))); } catch {}
        req.socket.end();
        return;
      }
      if (f.opcode === 0x1 || f.opcode === 0x2) {
        const text = f.payload.toString();
        if (text.startsWith('hello')) {
          try { req.socket.write(frame(0x1, Buffer.from('echo: ' + text))); } catch {}
        }
      }
    }
  });
}

function parseFrame(buf) {
  if (buf.length < 2) return null;
  const opcode = buf[0] & 0x0f, masked = buf[1] & 0x80;
  let len = buf[1] & 0x7f, off = 2;
  if (len === 126) { if (buf.length < 4) return null; len = buf.readUInt16BE(2); off = 4; }
  else if (len === 127) { if (buf.length < 10) return null; len = Number(buf.readBigUInt64BE(2)); off = 10; }
  let mask;
  if (masked) { if (buf.length < off + 4) return null; mask = buf.subarray(off, off + 4); off += 4; }
  if (buf.length < off + len) return null;
  const payload = Buffer.from(buf.subarray(off, off + len));
  if (masked) for (let i = 0; i < payload.length; i++) payload[i] ^= mask[i % 4];
  return { opcode, payload, consumed: off + len };
}

function frame(opcode, payload) {
  const len = payload.length;
  let head;
  if (len < 126) { head = Buffer.from([0x80 | opcode, len]); }
  else if (len < 65536) { head = Buffer.alloc(4); head[0] = 0x80 | opcode; head[1] = 126; head.writeUInt16BE(len, 2); }
  else { head = Buffer.alloc(10); head[0] = 0x80 | opcode; head[1] = 127; head.writeBigUInt64BE(BigInt(len), 2); }
  return Buffer.concat([head, payload]);
}

server.listen(PORT, '127.0.0.1', () => console.log(`[demo] http+sse+ws on http://127.0.0.1:${PORT} (/ /api/ping /sse /ws)`));
