# MyTunnel — Self-hosted ngrok alternative

Expose localhost services via your own VPS and domain, with a web dashboard to manage tunnels.

## Architecture

```
Internet → odoo.tunnel.yourdomain.com → [VPS Server :7891]
                                            ↕ WebSocket
                                         [Client on laptop]
                                            ↕
                                         localhost:8069

Dashboard: http://localhost:9000
```

## Setup

### 1. DNS (Domain Provider)

Add a wildcard A record pointing to your VPS:

```
*.tunnel.yourdomain.com  →  YOUR_VPS_IP
```

### 2. Build

```bash
# Server binary (for VPS)
GOOS=linux GOARCH=amd64 go build -o mytunnel-server ./cmd/server

# Client binary (for your laptop)
go build -o mytunnel-client ./cmd/client
```

### 3. Deploy Server (VPS)

```bash
# Copy binary to VPS
scp mytunnel-server user@your-vps:/usr/local/bin/

# Run (use systemd for production)
mytunnel-server -domain tunnel.yourdomain.com -token YOUR_SECRET -addr :7891
```

For HTTPS, put Nginx/Caddy in front:

```nginx
# /etc/nginx/sites-enabled/tunnel
server {
    listen 443 ssl;
    server_name *.tunnel.yourdomain.com;

    ssl_certificate     /etc/letsencrypt/live/tunnel.yourdomain.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/tunnel.yourdomain.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:7891;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
    }
}
```

Wildcard SSL with certbot:
```bash
certbot certonly --manual --preferred-challenges dns -d "*.tunnel.yourdomain.com"
```

### 4. Run Client (Laptop)

```bash
mytunnel-client -server wss://tunnel.hanayuvi.com -token YOUR_SECRET -ui :9000
```

token: SfGAUgb4qP7RLD1Zg6afDujZ9igXAqaLPfe7UYgu0Yq9PQkLkVFEpqpM1xLtuBCA
pm2 start ./mytunnel-server --name mytunnel -- -domain tunnel.domainmu.com -SfGAUgb4qP7RLD1Zg6afDujZ9igXAqaLPfe7UYgu0Yq9PQkLkVFEpqpM1xLtuBCA YOUR_SECRET -addr :7891
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
