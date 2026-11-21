# postitd deployen (postit.kryptikk.de)

`postitd` ist der Sync-Service für `p`. Er läuft als Docker-Container und lauscht auf `127.0.0.1:8080`; TLS und die Domain übernimmt der vorhandene Reverse-Proxy.

## 1. Auf dem Server

```sh
git clone … postit && cd postit      # oder das Repo anders auf den Server bringen
cp .env.example .env
echo "POSTIT_TOKEN=$(openssl rand -hex 32)" > .env
chmod 600 .env
docker compose up -d --build
curl -i http://127.0.0.1:8080/healthz   # → 204 No Content
```

Den Token aus `.env` brauchst du gleich auf jedem Gerät.

## 2. Reverse-Proxy

`postit.kryptikk.de` per HTTPS auf `http://127.0.0.1:8080` weiterleiten. Beispiele:

Caddy:
```
postit.kryptikk.de {
	reverse_proxy 127.0.0.1:8080
}
```

nginx (im `server`-Block mit TLS):
```
location / {
	proxy_pass http://127.0.0.1:8080;
	proxy_set_header Host $host;
	proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
	client_max_body_size 16m;
}
```

Läuft der Proxy selbst in Docker (z. B. Traefik), statt `ports:` den Container ins Proxy-Netzwerk hängen und den Proxy auf `postitd:8080` zeigen lassen.

## 3. Auf jedem Gerät

```sh
p -login https://postit.kryptikk.de   # fragt den Token ab, gleicht sofort ab
```

Die Verbindung landet in `~/.config/postit/sync.json` (nur für dich lesbar). Danach synchronisiert `p` von selbst: beim Start, kurz nach jeder Änderung, alle 30 s und beim Beenden. Offline arbeitet `p` normal weiter und holt den Abgleich nach. Manuell: `p -sync`.

## Betrieb

- **Update:** `git pull && docker compose up -d --build`
- **Logs:** `docker compose logs -f postitd`
- **Backup:** die SQLite-Datei im Volume sichern, am sichersten bei gestopptem Dienst:
  ```sh
  docker compose stop
  docker run --rm -v postit_postit-data:/data -v "$PWD":/backup alpine cp /data/postit.db /backup/postit-$(date +%F).db
  docker compose start
  ```
  (Der Volume-Name hängt vom Verzeichnisnamen ab: `docker volume ls`.)
- **Token wechseln:** neuen Token in `.env`, `docker compose up -d`, auf jedem Gerät erneut `p -login …`.

## Konfliktregel

Jede Zeile (Projekt, Post-it) trägt einen Änderungszeitpunkt. Ändern zwei Geräte dieselbe Zeile, gewinnt die spätere Änderung. Gelöschtes wird als Grabstein gespeichert, damit das Löschen auf allen Geräten ankommt.
