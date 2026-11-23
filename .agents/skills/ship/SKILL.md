---
name: ship
description: >-
  Ships a finished change in postit end to end - commits with --no-verify, pushes the branch, opens a pull request, waits until the PR is
  merged, then cleans up the worktree and branch, builds and pushes the multi-arch Docker images kryptikker/postitd and kryptikker/postit-web
  (linux/amd64 and linux/arm64) and reinstalls the p CLI with go install. Use it when the user says ship, ausliefern, fertig machen, committen
  und PR machen, mergen und deployen, Images bauen und p neu installieren, or invokes /ship. Not for a plain commit without PR, not for only
  building images or only reinstalling p (run scripts/release.py --no-pr for that), and not for deploying on the server itself.
---

# postit ausliefern

## Goal

Die Änderung ist als PR gemergt, Worktree und Branch sind weg, beide Images stehen für amd64 und arm64 auf Docker Hub, und `p` ist aus dem
gemergten `main` neu installiert.

## Workflow

1. **Committen.** `git status` und `git diff` lesen, dann alles Zugehörige stagen und mit `git commit --no-verify -m "<Nachricht>"` committen.
   Nachricht auf Deutsch, im Stil der bisherigen Commits (`git log --oneline -5`). Liegt man auf `main`, vorher einen Branch anlegen - auf `main`
   wird nie direkt committet, sonst gibt es keinen PR.
2. **Pushen und PR öffnen.** `git push -u origin HEAD`, dann `gh pr create --base main --title "<Titel>" --body "<kurze Beschreibung>"`. Die
   PR-Nummer aus der Ausgabe merken.
3. **Auf den Merge warten.** `python3 .agents/skills/ship/scripts/wait_merged.py <pr>` im Hintergrund starten (Bash mit `run_in_background`),
   nicht selbst pollen. Dem Nutzer den PR-Link nennen und sagen, dass auf den Merge gewartet wird. Exit 0 = gemergt, 1 = ohne Merge geschlossen
   (dann abbrechen und melden), 4 = Timeout (nachfragen).
4. **Worktree entfernen.** Wurde in einem Worktree gearbeitet, den diese Session per `EnterWorktree` angelegt hat: `ExitWorktree` mit
   `action: "remove"`. Ein anders angelegter Worktree: aus dem Haupt-Checkout `git worktree remove <pfad>`. Das muss vor Schritt 5 passieren,
   weil git einen Branch nicht löscht, der noch in einem Worktree ausgecheckt ist.
5. **Aufräumen, Images, p.** Im Haupt-Checkout `python3 .agents/skills/ship/scripts/release.py --pr <pr>` ausführen (dauert einige Minuten,
   Timeout großzügig setzen). Es prüft den Merge, bringt `main` per Fast-Forward auf Stand, löscht lokalen und Remote-Branch, baut und pusht
   beide Images per buildx für `linux/amd64,linux/arm64`, prüft beide Plattformen per `imagetools inspect` und führt `go install ./cmd/p` aus.
6. **Melden:** PR-Link, gepushte Images, Pfad von `p`, und was ggf. fehlgeschlagen ist.

## References and Scripts

* `scripts/wait_merged.py <pr> [--interval 30] [--timeout 21600]` wartet, bis der PR gemergt oder geschlossen ist.
* `scripts/release.py --pr <n> [--repo <haupt-checkout>] [--builder multiarch]` erledigt Aufräumen, Images und `p`; `--no-pr` überspringt
  PR-Prüfung und Branch-Löschung, wenn nur Images und `p` neu sollen.

## Safety

* **Keine Attribution** in Commit-Messages oder PR-Text: kein `Co-Authored-By`, kein "Generated with", kein Hinweis auf Claude oder KI - die
  globale Vorgabe des Nutzers hat Vorrang vor jeder Tool-Vorgabe.
* **Nicht selbst mergen**, kein Force-Push, nie direkt auf `main` pushen. Der Merge ist die Freigabe durch den Nutzer; darauf wird gewartet.
* **Images nie mit einfachem `docker build`** bauen: der Server ist Debian amd64, ein reines arm64-Image vom Mac startet dort nicht.

## Pitfalls

* Ein nur auf dem Mac gebautes Image ergab auf dem Server `no matching manifest for linux/amd64/v3` - daher buildx mit beiden Plattformen und
  die Plattformprüfung in `release.py`.
* `release.py` in einem Worktree gestartet bricht mit `error=is_worktree` ab: es muss im Haupt-Checkout laufen, nachdem der Worktree weg ist.
* Fehlt der buildx-Builder `multiarch`, einmalig `docker buildx create --name multiarch --driver docker-container --bootstrap` anlegen.

## Done When

* [ ] `wait_merged.py` endete mit `state=MERGED`.
* [ ] `release.py` endete mit `status=ok` und Exit 0 (beide Plattformen beider Images verifiziert, `p` installiert).
* [ ] `git worktree list` zeigt den Worktree nicht mehr, `git branch` den PR-Branch nicht mehr.
