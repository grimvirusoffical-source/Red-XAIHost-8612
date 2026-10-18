package host

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is the redxd configuration. Precedence: defaults < file < env < flags.
type Config struct {
	DataDir          string   `toml:"data_dir"`
	Listen           string   `toml:"listen"`         // default 127.0.0.1:7777
	NodeID           string   `toml:"node_id"`        // default: persisted random id
	Role             string   `toml:"role"`           // leader (default, alias standalone) | follower
	Peers            []string `toml:"peers"`          // follower: peers[0] is the leader URL
	Advertise        string   `toml:"advertise"`      // URL other nodes use to reach this node
	ClusterKey       string   `toml:"cluster_secret"` // optional shared secret for static clusters
	DefaultDB        string   `toml:"default_db"`     // db served at /v1/path/... (default "default")
	TLS              string   `toml:"tls"`            // off | auto | files
	TLSCert          string   `toml:"tls_cert"`
	TLSKey           string   `toml:"tls_key"`
	TLSHosts         []string `toml:"tls_hosts"`        // extra SANs for the auto cert
	RateRPS          float64  `toml:"rate_limit_rps"`   // per key default (0 => 100)
	RateBurst        int      `toml:"rate_limit_burst"` // per key default (0 => 2*rps)
	KeystoreEnc      bool     `toml:"keystore_encrypt"` // encrypt keystore at rest (needs REDX_KEYSTORE_PASSPHRASE)
	AuditLog         string   `toml:"audit_log"`        // default <data>/audit.log
	AuditOK          bool     `toml:"audit_success"`    // also log successful auths
	GRPCListen       string   `toml:"grpc_listen"`      // STUB: logged and ignored
	LogLevel         string   `toml:"log_level"`        // info | debug | quiet
	Heartbeat        int      `toml:"heartbeat_seconds"`
	MaxBodyMB        int      `toml:"max_body_mb"`
	MaxUploadMB      int      `toml:"max_upload_mb"`      // pack file uploads (PUT .../pack/.../file/...)
	EngineDurability string   `toml:"engine_durability"`  // sync | group (default) | async
	EngineDefaultTTL int      `toml:"engine_default_ttl"` // seconds applied to .red-xai-db-qs writes without TTL (0 = reject)
	ReplLogMax       int      `toml:"repl_log_max"`       // replication log entries retained (default 1,000,000)
	ArgonMemKiB      uint32   `toml:"argon_memory_kib"`   // keystore hashing cost (default 19456)
	ArgonTime        uint32   `toml:"argon_time"`

	// MasterKeyFile is a file holding the .red-xai-sec master key(s) (see `redxd keygen`). Only
	// the PATH may be configured; the key itself is never read from the environment or flags.
	// Default <data_dir>/master.key (loaded when it exists). Must not be group/world accessible.
	MasterKeyFile string `toml:"master_key_file"`
	// CodeAllowedBoxes limits which boxes .red-xai-code scripts may touch at all, as
	// "file:Box" globs ("players.red-xai.db:Player", "*.red-xai.db:Quest*"). Empty = scripts
	// cannot read or write any record. This is on top of the key's scope and the .red-xai-sec policy.
	CodeAllowedBoxes []string `toml:"code_allowed_boxes"`

	ConfigFile string `toml:"-"`
}

// DefaultDataDir returns a per-OS sensible default.
func DefaultDataDir() string {
	switch runtime.GOOS {
	case "windows":
		if pd := os.Getenv("ProgramData"); pd != "" {
			return filepath.Join(pd, "RedX", "data")
		}
	case "darwin":
		return "/usr/local/var/redx"
	}
	if os.Geteuid() == 0 {
		return "/var/lib/redx"
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".redx", "data")
	}
	return "./redx-data"
}

// DefaultConfig returns the built-in defaults.
func DefaultConfig() Config {
	return Config{
		DataDir:   DefaultDataDir(),
		Listen:    "127.0.0.1:7777",
		Role:      "leader",
		DefaultDB: "default",
		TLS:       "off",
		LogLevel:  "info",
		Heartbeat: 3,
		MaxBodyMB: 16, MaxUploadMB: 1024,
	}
}

// RegisterFlags binds flags onto cfg. Flags only override when explicitly set.
func (c *Config) RegisterFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.ConfigFile, "config", c.ConfigFile, "path to config file (TOML)")
	fs.StringVar(&c.DataDir, "data-dir", c.DataDir, "data directory")
	fs.StringVar(&c.Listen, "listen", c.Listen, "listen address")
	fs.StringVar(&c.NodeID, "node-id", c.NodeID, "node id (default: generated and persisted)")
	fs.StringVar(&c.Role, "role", c.Role, "leader|follower")
	fs.Var((*csv)(&c.Peers), "peers", "comma separated peer URLs (follower: first is the leader)")
	fs.StringVar(&c.Advertise, "advertise", c.Advertise, "URL other nodes use to reach this node")
	fs.StringVar(&c.TLS, "tls", c.TLS, "off|auto|files")
	fs.StringVar(&c.TLSCert, "tls-cert", c.TLSCert, "TLS certificate file (tls=files)")
	fs.StringVar(&c.TLSKey, "tls-key", c.TLSKey, "TLS key file (tls=files)")
	fs.StringVar(&c.DefaultDB, "default-db", c.DefaultDB, "database served by /v1/path/...")
	fs.BoolVar(&c.KeystoreEnc, "keystore-encrypt", c.KeystoreEnc, "encrypt keystore at rest (REDX_KEYSTORE_PASSPHRASE)")
	fs.StringVar(&c.MasterKeyFile, "master-key-file", c.MasterKeyFile, "file with the .red-xai-sec master key (default <data-dir>/master.key if present)")
	fs.Var((*csv)(&c.CodeAllowedBoxes), "code-allowed-boxes", "comma separated file:Box globs scripts may touch")
	fs.StringVar(&c.LogLevel, "log-level", c.LogLevel, "info|debug|quiet")
}

type csv []string

func (c *csv) String() string { return strings.Join(*c, ",") }
func (c *csv) Set(s string) error {
	*c = nil
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			*c = append(*c, p)
		}
	}
	return nil
}

// ApplyFile loads a TOML file over cfg (missing explicit file is an error).
func (c *Config) ApplyFile(path string, mustExist bool) error {
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) && !mustExist {
			return nil
		}
		return err
	}
	md, err := toml.DecodeFile(path, c)
	if err != nil {
		return fmt.Errorf("config %s: %w", path, err)
	}
	if u := md.Undecoded(); len(u) > 0 {
		return fmt.Errorf("config %s: unknown keys: %v", path, u)
	}
	return nil
}

// ApplyEnv overlays REDX_* environment variables.
func (c *Config) ApplyEnv(get func(string) string) error {
	s := func(name string, dst *string) {
		if v := get(name); v != "" {
			*dst = v
		}
	}
	s("REDX_DATA_DIR", &c.DataDir)
	s("REDX_LISTEN", &c.Listen)
	s("REDX_NODE_ID", &c.NodeID)
	s("REDX_ROLE", &c.Role)
	s("REDX_ADVERTISE", &c.Advertise)
	s("REDX_CLUSTER_SECRET", &c.ClusterKey)
	s("REDX_DEFAULT_DB", &c.DefaultDB)
	s("REDX_TLS", &c.TLS)
	s("REDX_TLS_CERT", &c.TLSCert)
	s("REDX_TLS_KEY", &c.TLSKey)
	s("REDX_LOG_LEVEL", &c.LogLevel)
	s("REDX_AUDIT_LOG", &c.AuditLog)
	s("REDX_GRPC_LISTEN", &c.GRPCListen)
	s("REDX_ENGINE_DURABILITY", &c.EngineDurability)
	s("REDX_MASTER_KEY_FILE", &c.MasterKeyFile) // a path, never the key
	if v := get("REDX_CODE_ALLOWED_BOXES"); v != "" {
		_ = (*csv)(&c.CodeAllowedBoxes).Set(v)
	}
	if v := get("REDX_PEERS"); v != "" {
		_ = (*csv)(&c.Peers).Set(v)
	}
	if v := get("REDX_TLS_HOSTS"); v != "" {
		_ = (*csv)(&c.TLSHosts).Set(v)
	}
	if v := get("REDX_KEYSTORE_ENCRYPT"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("REDX_KEYSTORE_ENCRYPT: %w", err)
		}
		c.KeystoreEnc = b
	}
	if v := get("REDX_RATE_LIMIT_RPS"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("REDX_RATE_LIMIT_RPS: %w", err)
		}
		c.RateRPS = f
	}
	return nil
}

// Validate normalises and checks the configuration.
func (c *Config) Validate() error {
	if c.Role == "standalone" || c.Role == "" {
		c.Role = "leader"
	}
	if c.Role != "leader" && c.Role != "follower" {
		return fmt.Errorf("role must be leader or follower, got %q", c.Role)
	}
	if c.Role == "follower" && len(c.Peers) == 0 {
		// A follower may also be configured purely via cluster.json (written by `redx node join`).
		if _, err := os.Stat(filepath.Join(c.DataDir, "cluster", "cluster.json")); err != nil {
			return fmt.Errorf("role=follower needs peers[0] (leader URL) or a cluster.json from `redx node join`")
		}
	}
	if !ValidDBName(c.DefaultDB) {
		return fmt.Errorf("invalid default_db %q", c.DefaultDB)
	}
	switch c.TLS {
	case "", "off":
		c.TLS = "off"
	case "auto":
	case "files":
		if c.TLSCert == "" || c.TLSKey == "" {
			return fmt.Errorf("tls=files needs tls_cert and tls_key")
		}
	default:
		return fmt.Errorf("tls must be off|auto|files, got %q", c.TLS)
	}
	if c.DataDir == "" {
		return fmt.Errorf("data_dir is required")
	}
	if c.RateRPS == 0 {
		c.RateRPS = 100
	}
	if c.RateBurst == 0 {
		c.RateBurst = int(c.RateRPS * 2)
	}
	if c.Heartbeat <= 0 {
		c.Heartbeat = 3
	}
	if c.MaxUploadMB <= 0 {
		c.MaxUploadMB = 1024
	}
	if c.MaxBodyMB <= 0 {
		c.MaxBodyMB = 16
	}
	if c.AuditLog == "" {
		c.AuditLog = filepath.Join(c.DataDir, "audit.log")
	}
	if c.ArgonMemKiB == 0 {
		c.ArgonMemKiB = 19456
	}
	if c.ArgonTime == 0 {
		c.ArgonTime = 2
	}
	return nil
}

// LoadConfig builds a Config from defaults, file, env and flags (in that order).
// args are command line arguments without the program name.
func LoadConfig(args []string, getenv func(string) string, fs *flag.FlagSet) (Config, error) {
	cfg := DefaultConfig()
	if fs == nil {
		fs = flag.NewFlagSet("redxd", flag.ContinueOnError)
	}
	// First pass: find --config so the file can sit below env+flags in precedence.
	pre := flag.NewFlagSet("pre", flag.ContinueOnError)
	pre.SetOutput(discard{})
	cf := pre.String("config", "", "")
	_ = pre.Parse(args)
	file := *cf
	if file == "" {
		file = getenv("REDX_CONFIG")
	}
	explicit := file != ""
	if file == "" {
		file = filepath.Join(cfg.DataDir, "redx.toml")
	}
	// data dir may itself come from env/flags; resolve env first for the default lookup.
	if !explicit {
		tmp := cfg
		_ = tmp.ApplyEnv(getenv)
		file = filepath.Join(tmp.DataDir, "redx.toml")
	}
	if err := cfg.ApplyFile(file, explicit); err != nil {
		return cfg, err
	}
	if err := cfg.ApplyEnv(getenv); err != nil {
		return cfg, err
	}
	cfg.RegisterFlags(fs)
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	return cfg, cfg.Validate()
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
