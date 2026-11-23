# postitd und Webapp deployen (postit.kryptikk.de)

Zwei Container aus `docker-compose.yml`:

| Dienst       | Aufgabe                                        | lauscht auf        |
|--------------|------------------------------------------------|--------------------|
| `postitd`    | Sync-Service für `p` (und für die Webapp)      | `127.0.0.1:8983`   |
| `postit-web` | Webapp im Browser, Login mit Passwort          | `127.0.0.1:8984`   |

TLS und die Domain übernimmt der vorhandene Reverse-Proxy. Die Webapp ist für `postitd` nur ein weiteres Gerät: Sie hat ihre eigene SQLite-Kopie und gleicht sie über das Compose-Netz (`http://postitd:8080`) ab, beim Start, kurz nach jeder Änderung, alle 15 s und vor dem Anzeigen einer Seite. Der Sync-Token bleibt auf dem Server, der Browser bekommt nur ein Session-Cookie.

## 1. Auf dem Server

```sh
git clone … postit && cd postit      # oder das Repo anders auf den Server bringen
cp .env.example .env
{ echo "POSTIT_TOKEN=$(openssl rand -hex 32)"; echo "POSTIT_WEB_PASSWORD=<dein Passwort, ≥ 12 Zeichen>"; } > .env
chmod 600 .env
docker compose up -d --build
curl -i http://127.0.0.1:8983/healthz   # postitd    → 204 No Content
curl -i http://127.0.0.1:8984/healthz   # postit-web → 204 No Content
```

Den Token aus `.env` brauchst du gleich auf jedem Gerät, das Passwort im Browser.

## 2. Reverse-Proxy

Eine Domain für beides: `/api/*` geht an `postitd`, alles andere an die Webapp. So bleibt `p -login https://postit.kryptikk.de` gültig.

Caddy:
```
postit.kryptikk.de {
	handle /api/* {
		reverse_proxy 127.0.0.1:8983
	}
	handle {
		reverse_proxy 127.0.0.1:8984
	}
}
```

nginx (im `server`-Block mit TLS):
```
location /api/ {
	proxy_pass http://127.0.0.1:8983;
	proxy_set_header Host $host;
	proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
	client_max_body_size 16m;
}
location / {
	proxy_pass http://127.0.0.1:8984;
	proxy_set_header Host $host;
	proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
	proxy_set_header X-Forwarded-Proto $scheme;
}
```

Wichtig für die Webapp: Der Proxy muss den ursprünglichen `Host` weitergeben (Caddy macht das von selbst). Die Webapp lehnt Formular-Posts ab, deren `Origin` nicht zum Host passt (CSRF-Schutz). `X-Forwarded-Proto: https` sorgt dafür, dass das Session-Cookie als `Secure` gesetzt wird.

Läuft der Proxy selbst in Docker (z. B. Traefik), statt `ports:` beide Container ins Proxy-Netzwerk hängen und den Proxy auf `postitd:8080` bzw. `postit-web:8080` zeigen lassen.

## 3. Auf jedem Gerät

```sh
p -login https://postit.kryptikk.de   # fragt den Token ab, gleicht sofort ab
```

Die Verbindung landet in `~/.config/postit/sync.json` (nur für dich lesbar). Danach synchronisiert `p` von selbst: beim Start, kurz nach jeder Änderung, alle 30 s und beim Beenden. Offline arbeitet `p` normal weiter und holt den Abgleich nach. Manuell: `p -sync`.

Im Browser: `https://postit.kryptikk.de` öffnen und mit `POSTIT_WEB_PASSWORD` anmelden. Die Sitzung hält 30 Tage.

## iPhone-Kurzbefehl: Text in die Inbox

`POST /api/v1/inbox` nimmt reinen Text als Body und legt ihn als gelbes Post-it in der Inbox ab (Antwort `201`). Gesichert ist der Endpunkt mit demselben Token wie der Sync.

```sh
curl -i -H "Authorization: Bearer $POSTIT_TOKEN" --data-binary 'Milch kaufen' https://postit.kryptikk.de/api/v1/inbox
```

In der App **Kurzbefehle** einen neuen Kurzbefehl anlegen, z. B. „Post-it“:

1. **Nach Eingabe fragen**: Eingabetyp *Text*, Frage „Post-it?“
2. **Inhalte von URL abrufen**: URL `https://postit.kryptikk.de/api/v1/inbox`. Unter *Mehr anzeigen*:
   - Methode: `POST`
   - Header: `Authorization` = `Bearer <Token aus .env>`
   - Request-Body: *Datei*, als Datei die Variable *Angegebene Eingabe*
3. Optional **Benachrichtigung anzeigen**: „In der Inbox ✓“

Danach kannst du den Kurzbefehl mit „Hey Siri, Post-it“ starten, ihn auf den Home-Bildschirm legen oder auf die Action-Taste. Soll er auch im Teilen-Menü erscheinen, schalte *Im Share-Sheet anzeigen* ein und nimm im Schritt 1 *Kurzbefehl-Eingabe* statt der Abfrage. Die Notiz liegt sofort auf dem Server, `p` und die Webapp zeigen sie beim nächsten Abgleich.

## Images bauen

Beide Images entstehen aus demselben `Dockerfile`:

```sh
docker build -t kryptikker/postitd:latest .                  # Standard (letzte Stage)
docker build --target web -t kryptikker/postit-web:latest .
docker push kryptikker/postitd:latest && docker push kryptikker/postit-web:latest
```

Auf dem Server reicht dann `docker compose pull && docker compose up -d`. Ohne Registry baut `docker compose up -d --build` die Webapp direkt aus dem Repo.

## Betrieb

- **Update:** `git pull && docker compose up -d --build` (oder `docker compose pull && docker compose up -d`)
- **Logs:** `docker compose logs -f postitd postit-web`
- **Backup:** Nur die SQLite-Datei von `postitd` ist wichtig. Am sichersten sicherst du sie bei gestopptem Dienst:
  ```sh
  docker compose stop
  docker run --rm -v postit_postit-data:/data -v "$PWD":/backup alpine cp /data/postit.db /backup/postit-$(date +%F).db
  docker compose start
  ```
  (Der Volume-Name hängt vom Verzeichnisnamen ab: `docker volume ls`.) Das Volume `postit-web-data` ist nur eine Kopie und braucht kein Backup. Löschst du es, holt sich die Webapp beim nächsten Start alles neu von `postitd`. Noch nicht abgeglichene Änderungen aus dem Browser gingen dabei verloren, deshalb vorher `docker compose stop postit-web`: Der Dienst synchronisiert beim Beenden ein letztes Mal.
- **Token wechseln:** neuen Token in `.env`, `docker compose up -d`, auf jedem Gerät erneut `p -login …`. Die Webapp übernimmt ihn automatisch, alle Browser-Sitzungen enden.
- **Web-Passwort wechseln:** `POSTIT_WEB_PASSWORD` in `.env` ändern, `docker compose up -d`. Alle Browser-Sitzungen enden.
- **Fehlversuche:** Nach 5 falschen Passwörtern pro IP und Minute sperrt die Webapp weitere Versuche für den Rest der Minute.

## Konfliktregel

Jede Zeile (Projekt, Post-it) trägt einen Änderungszeitpunkt. Ändern zwei Geräte dieselbe Zeile, gewinnt die spätere Änderung. Das gilt auch für die Webapp. Gelöschtes wird als Grabstein gespeichert, damit das Löschen auf allen Geräten ankommt.
