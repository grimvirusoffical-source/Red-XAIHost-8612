package host_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"redx/host"
)

func newKS(t *testing.T, pass string, now func() time.Time) (*host.Keystore, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "keys.red-xai-keys")
	ks, err := host.OpenKeystore(p, host.KeystoreOptions{Passphrase: pass, ArgonMem: 64, ArgonTime: 1, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return ks, p
}

func TestKeyFormatAndHashOnly(t *testing.T) {
	ks, path := newKS(t, "", nil)
	key, rec, err := ks.Create(host.CreateSpec{DB: "app", Scope: "write", Label: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^rx_live_[0-9a-zA-Z]{43}$`).MatchString(key) {
		t.Fatalf("bad key format %q", key)
	}
	if !strings.HasPrefix(key[len("rx_live_"):], rec.ID) {
		t.Fatal("id must be key prefix")
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), key[len("rx_live_"):]) || strings.Contains(string(raw), key) {
		t.Fatal("keystore file contains key material")
	}
	if !strings.Contains(string(raw), `"hash"`) || !strings.Contains(string(raw), `"salt"`) {
		t.Fatal("expected salted hash in keystore")
	}
	if st, _ := os.Stat(path); st.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Fatalf("keystore perms too open: %v", st.Mode())
	}
	got, err := ks.Verify(key)
	if err != nil || got.ID != rec.ID || got.Scope != "write" || got.DB != "app" {
		t.Fatalf("verify: %v %+v", err, got)
	}
	// two keys never share salt/hash
	_, r2, _ := ks.Create(host.CreateSpec{DB: "app", Scope: "read"})
	if r2.Salt == rec.Salt || r2.Hash == rec.Hash {
		t.Fatal("salt/hash reuse")
	}
}

func TestVerifyFailures(t *testing.T) {
	ks, _ := newKS(t, "", nil)
	key, _, _ := ks.Create(host.CreateSpec{Scope: "read"})
	cases := map[string]string{
		"empty":     "",
		"noprefix":  strings.TrimPrefix(key, "rx_live_"),
		"short":     key[:20],
		"badchars":  key[:len(key)-1] + "!",
		"wrongtail": key[:len(key)-1] + flip(key[len(key)-1]),
		"unknownid": "rx_live_" + strings.Repeat("A", 43),
	}
	for n, k := range cases {
		if r, err := ks.Verify(k); err == nil {
			t.Errorf("%s: expected failure, got %+v", n, r)
		}
	}
}

func flip(c byte) string {
	if c == 'a' {
		return "b"
	}
	return "a"
}

func TestRevokeExpireRotate(t *testing.T) {
	cur := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := func() time.Time { return cur }
	ks, _ := newKS(t, "", now)
	k1, r1, _ := ks.Create(host.CreateSpec{DB: "*", Scope: "admin", TTL: time.Hour, Label: "L"})
	if _, err := ks.Verify(k1); err != nil {
		t.Fatal(err)
	}
	cur = cur.Add(61 * time.Minute)
	if _, err := ks.Verify(k1); err != host.ErrKeyExpired {
		t.Fatalf("want expired, got %v", err)
	}
	cur = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// revoke is effective immediately even with warm verification cache
	k2, r2, _ := ks.Create(host.CreateSpec{Scope: "read"})
	if _, err := ks.Verify(k2); err != nil {
		t.Fatal(err)
	}
	if _, err := ks.Revoke(r2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ks.Verify(k2); err != host.ErrKeyRevoked {
		t.Fatalf("want revoked, got %v", err)
	}
	if _, err := ks.Revoke("nonexist"); err == nil {
		t.Fatal("revoke unknown should fail")
	}

	// rotate with grace: old valid until grace ends, new works, attrs copied
	k3, nr, err := ks.Rotate(r1.ID, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if nr.Scope != "admin" || nr.Label != "L" || nr.RotatedFrom != r1.ID || k3 == k1 {
		t.Fatalf("rotate attrs: %+v", nr)
	}
	if _, err := ks.Verify(k1); err != nil {
		t.Fatalf("old key should work during grace: %v", err)
	}
	cur = cur.Add(11 * time.Minute)
	if _, err := ks.Verify(k1); err == nil {
		t.Fatal("old key should be dead after grace")
	}
	if _, err := ks.Verify(k3); err != nil {
		t.Fatal(err)
	}
	// immediate rotate
	k4, _, err := ks.Rotate(nr.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ks.Verify(k3); err != host.ErrKeyRevoked {
		t.Fatalf("want revoked, got %v", err)
	}
	if _, err := ks.Verify(k4); err != nil {
		t.Fatal(err)
	}
}

func TestEncryptedAtRest(t *testing.T) {
	p := filepath.Join(t.TempDir(), "k")
	o := host.KeystoreOptions{Passphrase: "correct horse", ArgonMem: 64, ArgonTime: 1}
	ks, err := host.OpenKeystore(p, o)
	if err != nil {
		t.Fatal(err)
	}
	key, rec, _ := ks.Create(host.CreateSpec{Scope: "admin", Label: "secret-label-xyz"})
	raw, _ := os.ReadFile(p)
	if strings.Contains(string(raw), "secret-label-xyz") || strings.Contains(string(raw), rec.ID) || !strings.HasPrefix(string(raw), "REDXKS1") {
		t.Fatal("keystore not encrypted")
	}
	ks2, err := host.OpenKeystore(p, o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ks2.Verify(key); err != nil {
		t.Fatal(err)
	}
	o.Passphrase = "wrong"
	if _, err := host.OpenKeystore(p, o); err == nil {
		t.Fatal("wrong passphrase must fail")
	}
	o.Passphrase = ""
	if _, err := host.OpenKeystore(p, o); err == nil {
		t.Fatal("missing passphrase must fail")
	}
}

func TestSecondProcessChangesPickedUp(t *testing.T) {
	ks, p := newKS(t, "", nil)
	key, rec, _ := ks.Create(host.CreateSpec{Scope: "read"})
	if _, err := ks.Verify(key); err != nil {
		t.Fatal(err)
	}
	other, err := host.OpenKeystore(p, host.KeystoreOptions{ArgonMem: 64, ArgonTime: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Revoke(rec.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "revocation by another handle visible", 3*time.Second, func() bool {
		_, err := ks.Verify(key)
		return err == host.ErrKeyRevoked
	})
}

func TestScopesAndPrefix(t *testing.T) {
	r := host.KeyRecord{Scope: "write", DB: "app", Prefix: "users.red-xai.db:Box[ID=1]"}
	yes := []struct{ need, db, path string }{
		{"read", "app", "users.red-xai.db:Box[ID=1]"},
		{"write", "app", "users.red-xai.db:Box[ID=1].Name"},
	}
	no := []struct{ need, db, path string }{
		{"admin", "app", "users.red-xai.db:Box[ID=1]"},
		{"read", "other", "users.red-xai.db:Box[ID=1]"},
		{"read", "app", "users.red-xai.db:Box[ID=10]"},
		{"read", "app", "users.red-xai.db:Box[ID=1]x"},
		{"read", "app", "other.red-xai.db:Box[ID=1]"},
		{"read", "app", ""},
	}
	for _, c := range yes {
		if !r.Allows(c.need, c.db, c.path) {
			t.Errorf("should allow %+v", c)
		}
	}
	for _, c := range no {
		if r.Allows(c.need, c.db, c.path) {
			t.Errorf("should deny %+v", c)
		}
	}
	all := host.KeyRecord{Scope: "read", DB: "*"}
	if !all.Allows("read", "anything", "x") || all.Allows("write", "x", "x") {
		t.Error("wildcard db scope wrong")
	}
}

func TestParseTTL(t *testing.T) {
	for in, want := range map[string]time.Duration{"30d": 30 * 24 * time.Hour, "2w": 14 * 24 * time.Hour, "90m": 90 * time.Minute, "": 0, "never": 0} {
		got, err := host.ParseTTL(in)
		if err != nil || got != want {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	if _, err := host.ParseTTL("abc"); err == nil {
		t.Error("expected error")
	}
	if _, err := host.ParseTTL("-5d"); err == nil {
		t.Error("expected error")
	}
}

func TestInvalidCreate(t *testing.T) {
	ks, _ := newKS(t, "", nil)
	for _, sp := range []host.CreateSpec{{Scope: "root"}, {Scope: "read", DB: "../x"}, {Scope: ""}} {
		if _, _, err := ks.Create(sp); err == nil {
			t.Errorf("expected error for %+v", sp)
		}
	}
}

func TestRateLimiter(t *testing.T) {
	cur := time.Unix(0, 0)
	l := host.NewLimiter(func() time.Time { return cur })
	for i := 0; i < 3; i++ {
		if ok, _ := l.Allow("k", 1, 3); !ok {
			t.Fatal("burst should pass")
		}
	}
	if ok, w := l.Allow("k", 1, 3); ok || w <= 0 {
		t.Fatal("4th should be limited")
	}
	cur = cur.Add(2 * time.Second)
	if ok, _ := l.Allow("k", 1, 3); !ok {
		t.Fatal("should refill")
	}
	if ok, _ := l.Allow("other", 1, 1); !ok {
		t.Fatal("independent buckets")
	}
}
