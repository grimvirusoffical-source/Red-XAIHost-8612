// Command redxd is the RedX database host daemon.
//
//	redxd [run] [flags]          run in the foreground (default)
//	redxd install [flags]        install as an OS service (systemd / launchd / Windows service)
//	redxd uninstall|start|stop|restart|status
//	redxd version | --version
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/kardianos/service"
	"redx/host"
)

var svcCmds = map[string]bool{"install": true, "uninstall": true, "start": true, "stop": true, "restart": true, "status": true}

func main() {
	args := os.Args[1:]
	cmd := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	for _, a := range args {
		if a == "--version" || a == "-version" {
			cmd = "version"
		}
	}
	switch {
	case cmd == "version":
		fmt.Printf("redxd %s (%s/%s, %s, engine: %s)\n", host.Version, runtime.GOOS, runtime.GOARCH, runtime.Version(), engineName)
	case cmd == "keygen":
		if err := runKeygen(args); err != nil {
			fmt.Fprintln(os.Stderr, "redxd:", err)
			os.Exit(1)
		}
	case cmd == "run":
		if err := runForeground(args); err != nil {
			fmt.Fprintln(os.Stderr, "redxd:", err)
			os.Exit(1)
		}
	case svcCmds[cmd]:
		if err := serviceCommand(cmd, args); err != nil {
			fmt.Fprintln(os.Stderr, "redxd:", err)
			os.Exit(1)
		}
	case cmd == "help" || cmd == "-h" || cmd == "--help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `redxd - RedX database host

usage: redxd [run] [--config FILE] [--data-dir DIR] [--listen ADDR] [--role leader|follower]
             [--peers URL,...] [--node-id ID] [--advertise URL] [--tls off|auto|files] ...
       redxd install [--user] [run flags...]   install as a service (needs admin/root)
       redxd uninstall | start | stop | restart | status
       redxd keygen [--out FILE] [--id ID]     create a .red-xai-sec master key file (mode 0600)
       redxd version

Environment: REDX_CONFIG REDX_DATA_DIR REDX_LISTEN REDX_NODE_ID REDX_ROLE REDX_PEERS REDX_ADVERTISE
             REDX_TLS REDX_CLUSTER_SECRET REDX_KEYSTORE_PASSPHRASE REDX_NO_BOOTSTRAP
             REDX_MASTER_KEY_FILE (a path: the master key itself is never read from env or flags) ...
Precedence: defaults < config file < environment < flags. See docs/HOSTING.md.
`)
}

func makeLogf(level string, sink func(string)) func(string, ...interface{}) {
	if level == "quiet" {
		return func(string, ...interface{}) {}
	}
	return func(f string, a ...interface{}) { sink(fmt.Sprintf(f, a...)) }
}

func runForeground(args []string) error {
	if !service.Interactive() {
		// Started by the Windows SCM / systemd / launchd: speak the service protocol.
		return runAsService(args)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runDaemon(ctx, args, func(s string) { log.Print(s) })
}

// runDaemon loads config, opens the host and serves until ctx is done.
func runDaemon(ctx context.Context, args []string, sink func(string)) error {
	cfg, err := host.LoadConfig(args, os.Getenv, nil)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	logf := makeLogf(cfg.LogLevel, sink)
	op, err := opener(cfg, logf)
	if err != nil {
		return err
	}
	h, err := host.New(cfg, op, logf)
	if err != nil {
		return err
	}
	if h.BootstrapKey != "" {
		printBootstrap(h.BootstrapKey, cfg)
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		h.Close()
		return err
	}
	return h.Serve(ctx, ln)
}

func printBootstrap(key string, cfg host.Config) {
	// Printed once to the console; never written to any log file or the keystore.
	fmt.Println("==============================================================================")
	fmt.Println(" FIRST RUN: a one-time ADMIN API key was created. It is shown ONLY NOW.")
	fmt.Println(" Copy it somewhere safe (password manager). It cannot be recovered; if lost,")
	fmt.Println(" create a new admin key offline:  redx key create --offline --scope admin --db '*'")
	fmt.Println()
	fmt.Println("   " + key)
	fmt.Println()
	fmt.Println(" Use:  export REDX_KEY=<key>   then   redx key create --db default --scope write")
	fmt.Println("==============================================================================")
	_ = cfg
}

// ---- service support (kardianos/service) ----

type program struct {
	args   []string
	cancel context.CancelFunc
	done   chan error
	logger service.Logger
}

func (p *program) Start(s service.Service) error {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.done = make(chan error, 1)
	go func() {
		p.done <- runDaemon(ctx, p.args, func(m string) {
			if p.logger != nil {
				_ = p.logger.Info(m)
			}
		})
	}()
	return nil
}

func (p *program) Stop(s service.Service) error {
	if p.cancel != nil {
		p.cancel()
		<-p.done
	}
	return nil
}

func runAsService(args []string) error {
	p := &program{args: args}
	s, err := service.New(p, &service.Config{Name: "redxd", DisplayName: "RedX Database Host"})
	if err != nil {
		return err
	}
	if lg, err := s.Logger(nil); err == nil {
		p.logger = lg
	}
	return s.Run()
}

func serviceCommand(cmd string, args []string) error {
	user := false
	var rest []string
	for _, a := range args {
		if a == "--user" || a == "-user" {
			user = true
			continue
		}
		rest = append(rest, a)
	}
	// Resolve relative paths now: services start in a different working directory.
	for i, a := range rest {
		if (a == "--config" || a == "--data-dir" || a == "--tls-cert" || a == "--tls-key" || a == "--master-key-file") && i+1 < len(rest) {
			if abs, err := filepath.Abs(rest[i+1]); err == nil {
				rest[i+1] = abs
			}
		}
	}
	opts := service.KeyValue{"UserService": user, "RunAtLoad": true, "KeepAlive": true}
	cfg := &service.Config{
		Name: "redxd", DisplayName: "RedX Database Host", Description: "RedX database host (REST/WebSocket API, API keys, cluster node)",
		Arguments: append([]string{"run"}, rest...), Option: opts,
	}
	p := &program{args: rest}
	s, err := service.New(p, cfg)
	if err != nil {
		return err
	}
	if lg, err := s.Logger(nil); err == nil {
		p.logger = lg
	}
	switch cmd {
	case "install", "uninstall", "start", "stop", "restart":
		if err := service.Control(s, cmd); err != nil {
			return fmt.Errorf("%s: %w (installing/controlling services usually needs root/Administrator)", cmd, err)
		}
		fmt.Printf("service redxd: %s ok\n", cmd)
	case "status":
		st, err := s.Status()
		if err != nil {
			return err
		}
		names := map[service.Status]string{service.StatusRunning: "running", service.StatusStopped: "stopped", service.StatusUnknown: "unknown"}
		fmt.Println("service redxd:", names[st])
	}
	return nil
}
