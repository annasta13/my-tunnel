# MyTunnel — Self-hosted ngrok alternative

Expose localhost services via your own VPS and domain, with a web dashboard to manage tunnels.
Built with Go. Supports Cloudflare proxy with WebSocket keepalive.

## Architecture

```
Internet → odoo.tunnel.yourdomain.com → [Cloudflare] → [Nginx :80] → [Server :7891]
                                                                           ↕ WebSocket
                                                                        [Client on laptop]
                                                                           ↕
                                                                        localhost:8069

Dashboard: http://localhost:9000
```

## Setup

### 1. DNS (Cloudflare)

Add a wildcard A record:

```
Type: A
Name: *.tunnel
Value: YOUR_VPS_IP
Proxy: ON (orange cloud)
```

Set SSL/TLS mode to **Flexible** in Cloudflare dashboard.

### 2. Build

```bash
# Server binary (for Linux VPS)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o mytunnel-server ./cmd/server

# Client binary (for your laptop)
go build -o mytunnel-client ./cmd/client
```

### 3. Deploy Server (VPS)

#### Nginx config

```nginx
# /etc/nginx/sites-enabled/tunnel
server {
    listen 80;
    server_name *.tunnel.yourdomain.com;

    location / {
        proxy_pass http://127.0.0.1:7891;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_read_timeout 86400;
    }
}
```

#### Run with pm2

```bash
pm2 start ./mytunnel-server --name mytunnel -- -domain yourdomain.com -token YOUR_SECRET -addr :7891
```

Or use an ecosystem file:

```js
// ecosystem.config.js
module.exports = {
  apps: [{
    name: "mytunnel",
    script: "./mytunnel-server",
    args: "-domain yourdomain.com -token YOUR_SECRET -addr :7891",
    cwd: "/path/to/app"
  }]
}
```

```bash
pm2 start ecosystem.config.js
pm2 save && pm2 startup
```

### 4. Run Client (Laptop)

```bash
./mytunnel-client -server wss://tunnel.yourdomain.com -token YOUR_SECRET -ui :9000
```

Open http://localhost:9000 to manage tunnels.

## Usage (Dashboard)

1. Open http://localhost:9000
2. Enter subdomain name (e.g. `odoo`) and local port (e.g. `8069`)
3. Click **+ Add Tunnel**
4. Access via `https://odoo.tunnel.yourdomain.com`
5. Use **Pause/Resume/Delete** buttons to manage

## Flags

### Server
| Flag | Default | Description |
|------|---------|-------------|
| `-domain` | `tunnel.example.com` | Base domain |
| `-token` | `changeme` | Auth token |
| `-addr` | `:7891` | Listen address |

### Client
| Flag | Default | Description |
|------|---------|-------------|
| `-server` | `ws://tunnel.example.com:7891` | Server WebSocket URL |
| `-token` | `changeme` | Auth token |
| `-ui` | `:9000` | Dashboard address |

## Notes

- Ping/pong keepalive runs every 30s to prevent Cloudflare idle timeout (~100s)
- Tunnels auto-reconnect on disconnect
- Token is shared secret between server and client — use a strong random string
