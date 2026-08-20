# PIK.TAK 协议

传输:TCP + 换行分隔 JSON(v0;WebSocket 是另一个 `wire.FrameConn` 实现)。
控制面帧是一个 JSON 对象占一行。信封:

```json
{"t":"<type>","tun":"<tunnelId>","id":<raw json>,"p":<opaque>}
```

- `t` 帧类型。中统能看懂的只有下面列出的控制帧;其余全部当不透明载荷转发。
- `tun` 隧道 id(piktak-native 模式用)。带 `tun` 的帧按 id 在 host↔client 间路由。
- `id` 关联 id(JSON 任意值)。中继原样带过,适配器原样回填。
- `p` 不透明载荷。中继**只**对它自己拥有的几个控制帧解码,其余不碰。

relay 跑两个监听:**wire 端口**(控制 + data.attach,换行 JSON)和 **ingress 端口**(裸 TCP,应用面,如浏览器)。wire 端口上每条新连接先读一行,按 `t` 分派:`hello` → 控制会话,`data.attach` → 裸数据通道。ingress 端口不做任何 framing,纯裸字节。

## L0 身份与配对(机器码)

`hello` 里带 `code`——一段短的人可输入的机器码,relay 用 `PIKTAK_CODES` 白名单校验。码同时充当 host 身份(未声明 hostId 时,hostId = code)。这就是"远程发现"那一半:client 用 hostId 点名要连哪个 host。

```
→ {"t":"hello","p":{"role":"host"|"client","code":"12345678","hostId":"host-1"}}
← {"t":"hello.ok","p":{"hostId":"..."}}
← {"t":"hello.err","p":{"code":"UNAUTHORIZED|BAD_ROLE|HOST_NOT_FOUND"}}
```

PSK 作为另一种 `Pairer` 实现保留;换 L0 策略不碰 L1。

## 模式 A:piktak-native(结构化适配器)

两端都说 PIK.TAK 信封,relay 按 `tun` 路由不透明帧。

```
client → relay → host: {"t":"tun.open","id":"1"}          # relay 分配 tun 后转给 host
host → client:         {"t":"tun.ok","id":"1","tun":"t1"}
host → client:         {"t":"neg","tun":"t1","p":{"adapter":"echo","version":1,"caps":["echo"]}}
client → host:         {"t":"neg.ack","tun":"t1","p":{"ok":true,"version":1}}
之后任意 {"tun":"t1",...}: 适配器自定义,relay 透传
```

L3 适配器不填 `tun`;host 驱动在 send 回调里盖 tunnel 戳。适配器只管自己的词表(`echo`/`echoed`)。

## 模式 B:透明(裸字节管道,对两端无感)

host 暴露一个本地 TCP 地址(比如 dsh web 的 `127.0.0.1:7531`)到 relay 的 ingress 端口。浏览器/应用打 ingress,relay 把它的裸字节逐字节 splice 到 host 按需开启的数据通道,再由 host splice 到本地目标。**relay 不解析 HTTP、不解析任何应用字节**,两端(dsh / 浏览器)不知道 PIK.TAK 存在。

```
host → relay:  {"t":"port.expose","p":{"local":"127.0.0.1:7531"}}
relay → host:  {"t":"port.ok","p":{"ingress":":7682"}}
[浏览器连 ingress :7682,裸 TCP]
relay → host:  {"t":"inbound","p":{"connId":"c1"}}
host → relay (新连接): {"t":"data.attach","p":{"connId":"c1"}}
relay → host:  {"t":"data.ack","p":{"connId":"c1"}}
之后该连接 = 裸字节双向 splice:浏览器 ↔ relay ↔ host ↔ 127.0.0.1:7531
```

`data.attach` 后连接切裸字节:relay 读到这一行 → 回 `data.ack` → 用 `wire.TCPConn.Raw()` 拿到底层 conn + bufio reader 直接 `io.Copy`。host 端同理,把数据通道和本地 `127.0.0.1:7531` 互拷。因为 host 等 `data.ack` 后才发裸字节,bufio 不会 over-read;`Raw()` 的 reader 也会先排空已缓冲字节,稳。

### 透明性的边界与安全

- 透明是**配对后**才成立的:控制帧(`hello`/`port.expose`/`data.attach`)还是 PIK.TAK 的;一旦裸数据通道接上,两端之间就是哑管道。
- ingress 一开,谁能到这个公网端口,谁就等于穿透到 host 的受信 loopback(对 dsh 来说等于公开 RCE)。所以 ingress 必须有门禁:每条入站过 L0、或路径带 token、或 IP 白名单、或 host 只接受自己发起的 tunnel。机器码白名单是当前的门;更强的(账号 + 一次性码、邀请 token)是后续 `Pairer`。

## 不变量

- `internal/relay` 只 import `wire` 和 `l0`,不 import `l2`/`l3`。
  `go list -f '{{join .Imports "\n"}}' ./internal/relay | grep github.com/charlzyx/piktak` 只输出这两条。
- 透明模式下 relay 对应用协议一无所知(HTTP/WS 都是字节),比 piktak-native 更不透明,不变量不破。
- `internal/bridge` 只 import `wire` + stdlib;`internal/host` import `wire`+`l2`+`l3`;两者都不 import relay。两端驱动只 share wire 契约。
