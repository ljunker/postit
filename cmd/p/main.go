// Command p shows post-its in a terminal UI.
//
//	p                  open the UI
//	p <text …>         put a new post-it into the Inbox
//	p -login <server>  connect to a postitd sync server
//	p -sync            sync once with the server
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/term"

	"postit/md"
	"postit/store"
	"postit/syncclient"
	"postit/ui"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "p:", err)
		os.Exit(1)
	}
}

func run() error {
	dbPath := flag.String("db", "", "Pfad zur Datenbank (Standard: $POSTIT_DB oder ~/.local/share/postit/postit.db)")
	login := flag.String("login", "", "mit dem Sync-Server unter dieser URL verbinden, z. B. https://postit.kryptikk.de")
	syncOnce := flag.Bool("sync", false, "einmal mit dem Server abgleichen")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Aufruf:\n  p                  Post-its anzeigen\n  p <text …>         neues Post-it in die Inbox legen (\\n = Zeilenumbruch)\n  p -login <url>     mit Sync-Server verbinden\n  p -sync            jetzt abgleichen\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	path := *dbPath
	if path == "" {
		path = os.Getenv("POSTIT_DB")
	}
	if path == "" {
		var err error
		if path, err = store.DefaultPath(); err != nil {
			return err
		}
	}
	st, err := store.Open(path)
	if err != nil {
		return err
	}
	defer st.Close()

	if *login != "" {
		return doLogin(st, *login)
	}

	cfg, err := syncclient.LoadConfig()
	if err != nil {
		return fmt.Errorf("Sync-Konfiguration: %w", err)
	}
	var client *syncclient.Client
	if cfg.Enabled() {
		client = syncclient.New(cfg)
	}

	if *syncOnce {
		if client == nil {
			return fmt.Errorf("Sync ist nicht eingerichtet – zuerst: p -login https://postit.kryptikk.de")
		}
		return doSync(st, client)
	}

	if flag.NArg() > 0 {
		if err := quickAdd(st, strings.Join(flag.Args(), " ")); err != nil {
			return err
		}
		syncQuietly(st, client, 3*time.Second) // otherwise the next run catches up
		return nil
	}

	var syncFn ui.SyncFunc
	if client != nil {
		syncFn = func(ctx context.Context) (bool, error) {
			res, err := client.Sync(ctx, st)
			return res.Changed, err
		}
	}
	_, err = tea.NewProgram(ui.New(st, syncFn), tea.WithAltScreen()).Run()
	syncQuietly(st, client, 3*time.Second)
	return err
}

func quickAdd(st *store.Store, text string) error {
	body := strings.TrimSpace(strings.ReplaceAll(text, `\n`, "\n"))
	if body == "" {
		return fmt.Errorf("leerer Text")
	}
	inbox, err := st.InboxID()
	if err != nil {
		return err
	}
	if _, err := st.CreateNote(inbox, body, ""); err != nil {
		return err
	}
	fmt.Printf("➜ %s: %s\n", store.InboxName, md.Title(body))
	return nil
}

// syncQuietly sends pending local changes, ignoring errors.
func syncQuietly(st *store.Store, client *syncclient.Client, timeout time.Duration) {
	if client == nil {
		return
	}
	if pending, err := st.HasPending(); err != nil || !pending {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, _ = client.Sync(ctx, st)
}

func doSync(st *store.Store, client *syncclient.Client) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := client.Sync(ctx, st)
	if err != nil {
		return err
	}
	fmt.Printf("✓ abgeglichen: %d gesendet, %d empfangen\n", res.Pushed, res.Pulled)
	return nil
}

func doLogin(st *store.Store, server string) error {
	server = strings.TrimRight(strings.TrimSpace(server), "/")
	if !strings.HasPrefix(server, "https://") && !strings.HasPrefix(server, "http://") {
		server = "https://" + server
	}
	token, err := readToken()
	if err != nil {
		return err
	}
	cfg := syncclient.Config{Server: server, Token: token}
	client := syncclient.New(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := client.Ping(ctx); err != nil {
		return fmt.Errorf("Verbindung zu %s: %w", server, err)
	}
	if err := syncclient.SaveConfig(cfg); err != nil {
		return err
	}
	path, _ := syncclient.ConfigPath()
	fmt.Printf("✓ verbunden mit %s (gespeichert in %s)\n", server, path)
	return doSync(st, client)
}

// readToken asks for the token without echo, or reads it from a pipe.
func readToken() (string, error) {
	var token string
	if term.IsTerminal(os.Stdin.Fd()) {
		fmt.Fprint(os.Stderr, "Token: ")
		b, err := term.ReadPassword(os.Stdin.Fd())
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		token = string(b)
	} else {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", err
		}
		token = line
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", fmt.Errorf("kein Token angegeben")
	}
	return token, nil
}
