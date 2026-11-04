package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const decoy = "INERT-FIXTURE-SECRET-DO-NOT-EMIT"

var fixtureOnce sync.Once
var fixtureData []byte
var fixtureError error

// Build only. Neither this ELF program nor any production binary is executed.
func fixture(t *testing.T) []byte {
	t.Helper()
	fixtureOnce.Do(func() {
		root := t.TempDir()
		pkg := filepath.Join(root, "src", "redx", "host")
		main := filepath.Join(root, "src", "redx", "cmd", "probe")
		for _, p := range []string{pkg, main, filepath.Join(root, "home"), filepath.Join(root, "tmp")} {
			if err := os.MkdirAll(p, 0700); err != nil {
				fixtureError = err
				return
			}
		}
		files := map[string]string{
			filepath.Join(pkg, "mail.go"):  "package host\n//go:noinline\nfunc SMTPRelayNameProbe() string { return \"" + decoy + "\" }\n",
			filepath.Join(main, "main.go"): "package main\nimport (\"fmt\"; \"redx/host\")\nfunc main(){ fmt.Println(host.SMTPRelayNameProbe()) }\n",
		}
		for p, s := range files {
			if err := os.WriteFile(p, []byte(s), 0600); err != nil {
				fixtureError = err
				return
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		binaryPath := filepath.Join(root, "probe-linux")
		cmd := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-buildvcs=false", "-trimpath", "-ldflags=-s -w", "-o", binaryPath, "redx/cmd/probe")
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "SystemRoot=" + os.Getenv("SystemRoot"), "GOROOT=" + runtime.GOROOT(), "GOPATH=" + root, "GOCACHE=" + filepath.Join(root, "cache"), "GOMODCACHE=" + filepath.Join(root, "modcache"), "GOENV=off", "GOWORK=off", "GO111MODULE=off", "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64", "HOME=" + filepath.Join(root, "home"), "USERPROFILE=" + filepath.Join(root, "home"), "TEMP=" + filepath.Join(root, "tmp"), "TMP=" + filepath.Join(root, "tmp")}
		if _, err := cmd.CombinedOutput(); err != nil {
			fixtureError = err
			return
		}
		fixtureData, fixtureError = os.ReadFile(binaryPath)
	})
	if fixtureError != nil {
		t.Fatal("owned synthetic fixture build failed:", fixtureError)
	}
	return bytes.Clone(fixtureData)
}

func hash(data []byte) []byte { sum := sha256.Sum256(data); return sum[:] }
func commandImage(t *testing.T, image []byte, expected []byte) (int, response, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fixture-elf")
	if err := os.WriteFile(p, image, 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	code := run([]string{"--binary", p, "--expected-sha256", hex.EncodeToString(expected)}, &out)
	var r response
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatalf("JSON response required: %v", err)
	}
	if out.Len() > maxOutputBytes || strings.Contains(out.String(), p) || strings.Contains(out.String(), decoy) {
		t.Fatal("private path/decoy or oversized output")
	}
	return code, r, out.String()
}

func TestOwnedStrippedELFNamesOnly(t *testing.T) {
	image := fixture(t)
	ef, err := elf.NewFile(bytes.NewReader(image))
	if err != nil {
		t.Fatal(err)
	}
	defer ef.Close()
	if ef.Section(".symtab") != nil {
		t.Fatal("fixture must be stripped")
	}
	if !bytes.Contains(image, []byte(decoy)) {
		t.Fatal("decoy must exist in fixture image")
	}
	code, r, raw := commandImage(t, image, hash(image))
	if code != 0 || !r.OK || r.Result == nil {
		t.Fatalf("expected success, got %+v", r)
	}
	if !reflect.DeepEqual(r.Result.Names, []string{"redx/host.SMTPRelayNameProbe"}) {
		t.Fatalf("unexpected selected names: %v", r.Result.Names)
	}
	if !r.Result.StaticOnly || r.Result.RuntimePrecedence || r.Result.Functions < 1 {
		t.Fatal("static-only result boundary")
	}
	for _, forbidden := range []string{"source_path", "source_line", "address", "environment", "password", "fixture/main.go"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("unexpected output field %s", forbidden)
		}
	}
}

func TestHashMismatchDoesNotRevealComputedHash(t *testing.T) {
	image := fixture(t)
	code, r, raw := commandImage(t, image, make([]byte, 32))
	if code != 1 || r.OK || r.Error != "hash_mismatch" || r.Result != nil || strings.Contains(raw, hex.EncodeToString(hash(image))) {
		t.Fatal("hash mismatch must refuse without metadata/hash disclosure")
	}
}

func TestArgumentsAndNonRegularFileRefuse(t *testing.T) {
	for _, args := range [][]string{nil, {"--binary", "relative", "--expected-sha256", strings.Repeat("0", 64)}, {"--binary", t.TempDir(), "--expected-sha256", "not-a-hash"}, {"--binary", t.TempDir(), "--expected-sha256", strings.Repeat("0", 64)}} {
		var out bytes.Buffer
		if run(args, &out) != 1 {
			t.Fatal("must refuse")
		}
		var r response
		if json.Unmarshal(out.Bytes(), &r) != nil || r.OK || r.Result != nil {
			t.Fatal("sanitized refusal JSON required")
		}
	}
}

func TestOversizedFileRefusesBeforeReading(t *testing.T) {
	p := filepath.Join(t.TempDir(), "oversized")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(maxFileBytes + 1); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	r := execute([]string{"--binary", p, "--expected-sha256", strings.Repeat("0", 64)})
	if r.Error != "input_limit" || r.OK || r.Result != nil {
		t.Fatal("oversized input must refuse")
	}
}

func TestMalformedELFHeadersAndMetadataRefuse(t *testing.T) {
	original := fixture(t)
	cases := map[string]func([]byte){
		"elf32": func(b []byte) { b[4] = 1 }, "big-endian": func(b []byte) { b[5] = 2 }, "wrong-machine": func(b []byte) { binary.LittleEndian.PutUint16(b[18:], 3) },
		"extended-section-count": func(b []byte) { binary.LittleEndian.PutUint16(b[60:], 0) },
		"section-count-cap":      func(b []byte) { binary.LittleEndian.PutUint16(b[60:], maxSections+1) },
		"header-range-overflow":  func(b []byte) { binary.LittleEndian.PutUint64(b[40:], ^uint64(0)-32) },
		"wrong-entry-size":       func(b []byte) { binary.LittleEndian.PutUint16(b[58:], 63) },
		"extended-name-index":    func(b []byte) { binary.LittleEndian.PutUint16(b[62:], 0xffff) },
		"compressed-name-table": func(b []byte) {
			off := binary.LittleEndian.Uint64(b[40:]) + uint64(binary.LittleEndian.Uint16(b[62:]))*64
			binary.LittleEndian.PutUint64(b[off+8:], uint64(elf.SHF_COMPRESSED))
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			b := bytes.Clone(original)
			change(b)
			if r, err := inspect(b, hash(b)); err == nil || r != nil {
				t.Fatal("malformed/unsupported metadata accepted")
			}
		})
	}
	for _, n := range []int{0, 1, 4, 15, 63, 64, len(original) - 1} {
		t.Run("truncation", func(t *testing.T) {
			b := original[:n]
			if r, err := inspect(b, hash(b)); err == nil || r != nil {
				t.Fatal("truncated image accepted")
			}
		})
	}
}

func syntheticPCLN(names []string) []byte {
	var stringsTable []byte
	var offsets []uint32
	for _, name := range names {
		offsets = append(offsets, uint32(len(stringsTable)))
		stringsTable = append(stringsTable, []byte(name)...)
		stringsTable = append(stringsTable, 0)
	}
	namesEnd := 72 + len(stringsTable)
	tableBytes := 8*len(names) + 4
	p := make([]byte, namesEnd+tableBytes+8*len(names))
	binary.LittleEndian.PutUint32(p, go120Magic)
	p[6] = 1
	p[7] = 8
	binary.LittleEndian.PutUint64(p[8:], uint64(len(names)))
	for _, off := range []int{32, 40, 48, 56, 64} {
		value := uint64(namesEnd)
		if off == 32 {
			value = 72
		}
		binary.LittleEndian.PutUint64(p[off:], value)
	}
	copy(p[72:], stringsTable)
	d := p[namesEnd:]
	for i, off := range offsets {
		record := tableBytes + i*8
		binary.LittleEndian.PutUint32(d[i*8:], uint32(i*16))
		binary.LittleEndian.PutUint32(d[i*8+4:], uint32(record))
		binary.LittleEndian.PutUint32(d[record:], uint32(i*16))
		binary.LittleEndian.PutUint32(d[record+4:], off)
	}
	binary.LittleEndian.PutUint32(d[len(names)*8:], uint32(len(names)*16))
	return p
}

func TestStrictNamesAndFocusFilter(t *testing.T) {
	p := syntheticPCLN([]string{"redx/host.(*Mailer).sendRelay.func1", "main.SMTPProbe", "net/smtp.send", "redx/host.ordinary", "redx/host.Mail\nControl", "redx/host.Mail<markup>", "redx/host." + strings.Repeat("x", 600) + "Mail"})
	names, _, suppressed, err := functionNames(p, 10000)
	if err != nil || !reflect.DeepEqual(names, []string{"main.SMTPProbe", "redx/host.(*Mailer).sendRelay.func1"}) || suppressed == 0 {
		t.Fatalf("strict filter failed: %v %v", names, err)
	}
}

func TestZeroExtentFunctionNameIsAccepted(t *testing.T) {
	p := syntheticPCLN([]string{"redx/host.sendMail"})
	d := p[binary.LittleEndian.Uint64(p[64:]):]
	binary.LittleEndian.PutUint32(d[8:], 0)
	names, _, _, err := functionNames(p, 10000)
	if err != nil || !reflect.DeepEqual(names, []string{"redx/host.sendMail"}) {
		t.Fatalf("valid equal adjacent offsets refused: %v %v", names, err)
	}
}

func TestPCLNBoundsAndLimits(t *testing.T) {
	base := syntheticPCLN([]string{"redx/host.sendMail"})
	cases := map[string]func([]byte){
		"old-magic":               func(p []byte) { binary.LittleEndian.PutUint32(p, 0xfffffffa) },
		"wrong-pointer-size":      func(p []byte) { p[7] = 4 },
		"function-count-overflow": func(p []byte) { binary.LittleEndian.PutUint64(p[8:], ^uint64(0)) },
		"file-count-cap":          func(p []byte) { binary.LittleEndian.PutUint64(p[16:], maxFiles+1) },
		"offset-overflow":         func(p []byte) { binary.LittleEndian.PutUint64(p[32:], ^uint64(0)) },
		"backwards-offset":        func(p []byte) { binary.LittleEndian.PutUint64(p[40:], 71) },
		"record-inside-table":     func(p []byte) { d := p[binary.LittleEndian.Uint64(p[64:]):]; binary.LittleEndian.PutUint32(d[4:], 0) },
		"negative-name-offset": func(p []byte) {
			d := p[binary.LittleEndian.Uint64(p[64:]):]
			r := binary.LittleEndian.Uint32(d[4:])
			binary.LittleEndian.PutUint32(d[r+4:], 0xffffffff)
		},
		"name-out-of-region": func(p []byte) {
			d := p[binary.LittleEndian.Uint64(p[64:]):]
			r := binary.LittleEndian.Uint32(d[4:])
			binary.LittleEndian.PutUint32(d[r+4:], 100000)
		},
		"interior-name-offset": func(p []byte) {
			d := p[binary.LittleEndian.Uint64(p[64:]):]
			r := binary.LittleEndian.Uint32(d[4:])
			binary.LittleEndian.PutUint32(d[r+4:], 1)
		},
		"unterminated-short-name": func(p []byte) { p[binary.LittleEndian.Uint64(p[40:])-1] = 'x' },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			p := bytes.Clone(base)
			change(p)
			if _, _, _, err := functionNames(p, 10000); err == nil {
				t.Fatal("invalid metadata accepted")
			}
		})
	}
	var names []string
	for i := 0; i < maxNames+1; i++ {
		names = append(names, "redx/host.sendMail"+strings.Repeat("x", i))
	}
	if _, _, _, err := functionNames(syntheticPCLN(names), 10000); err == nil || err.Error() != "match_limit" {
		t.Fatal("match count cap required")
	}
}

func TestArbitraryShortInputsCannotPanic(t *testing.T) {
	for n := 0; n < 256; n++ {
		b := bytes.Repeat([]byte{byte(n)}, n)
		if r, err := inspect(b, hash(b)); err == nil || r != nil {
			t.Fatal("arbitrary non-ELF accepted")
		}
	}
}
