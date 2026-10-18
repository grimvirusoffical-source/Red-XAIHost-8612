package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"

	"github.com/gorilla/websocket"
)

func subscribe(g globals, path string) {
	u, err := url.Parse(g.addr)
	if err != nil {
		die("%v", err)
	}
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	if g.db != "" {
		u.Path = "/v1/db/" + g.db + "/subscribe"
	} else {
		u.Path = "/v1/subscribe"
	}
	u.RawQuery = url.Values{"path": {path}}.Encode()
	d := *websocket.DefaultDialer
	if g.insecure {
		d.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	conn, resp, err := d.Dial(u.String(), http.Header{"Authorization": {"Bearer " + g.key}})
	if err != nil {
		if resp != nil {
			die("subscribe refused: %s", resp.Status)
		}
		die("%v", err)
	}
	defer conn.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() { <-ctx.Done(); conn.Close() }()
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() == nil {
				fmt.Fprintln(os.Stderr, "connection closed:", strings.TrimSpace(err.Error()))
			}
			return
		}
		fmt.Println(string(msg))
	}
}
