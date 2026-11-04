package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T, source string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(source), 0600); err != nil {
		t.Fatal("fixture create failed")
	}
	return dir
}
func encoded(t *testing.T, r *report) string {
	t.Helper()
	bytes, err := json.Marshal(r)
	if err != nil {
		t.Fatal("marshal failed")
	}
	return string(bytes)
}
func TestLiteralPrivacyAndAllowedMetadata(t *testing.T) {
	secrets := []string{"RX_LIVE_PRIVATE_TOKEN_7z9q3", "planted-password-not-an-env-name", "raw-private-string-secret", "comment-private-secret", "struct-tag-secret", "import-credential-secret", "route-private-secret"}
	source := "package main\nimport (\n\"os\"\n\"net/http\"\n storage \"redx/engine\"\n invalid \"https://user:import-credential-secret@example.invalid/pkg\"\n)\n" +
		"// comment-private-secret\nconst token=\"RX_LIVE_PRIVATE_TOKEN_7z9q3\"\nvar password=\"planted-password-not-an-env-name\"\n" +
		"type Service struct { Password string `json:\"struct-tag-secret\"`; Store *storage.Store }\n" +
		"func (s *Service) Close() {\n defer s.Store.Close()\n go func(){ s.Store.Backup(`raw-private-string-secret`); _=os.Getenv(\"REDX_ACCOUNTS_DATA\") }()\n" +
		" http.HandleFunc(\"/api/accounts/me\",nil)\n http.Handle(\"/api/accounts/route-private-secret\",nil)\n _=os.Getenv(\"planted-password-not-an-env-name\")\n factory(\"RX_LIVE_PRIVATE_TOKEN_7z9q3\").Close()\n}\n"
	dir := fixture(t, source)
	r, err := inspectDirectory(context.Background(), dir)
	if err != nil {
		t.Fatalf("fixture inspection failed: %T", err)
	}
	output := encoded(t, r)
	for _, secret := range secrets {
		if strings.Contains(output, secret) {
			t.Fatal("planted private literal escaped metadata filter")
		}
	}
	if len(r.Files) != 1 || r.Files[0].WithheldImports != 1 || r.Files[0].WithheldRoutes != 1 || r.Files[0].WithheldEnvironmentArguments != 1 {
		t.Fatal("withheld counts incorrect")
	}
	digest := sha256.Sum256([]byte(source))
	if r.Files[0].SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatal("source digest mismatch")
	}
	if !strings.Contains(output, `"reviewedRoute":"/api/accounts/me"`) || !strings.Contains(output, `"environmentName":"REDX_ACCOUNTS_DATA"`) || !strings.Contains(output, `"localRepoRelativeLead":"engine"`) {
		t.Fatal("reviewed metadata missing")
	}
	if r.SourceBinaryParityProven || r.BuildConstraintsEvaluated || !r.ReferenceVCSModified {
		t.Fatal("unsupported provenance claim")
	}
	if len(r.LocalPackageLeads) != 1 || r.LocalPackageLeads[0] != "engine" {
		t.Fatal("local package leads incorrect")
	}
}
func TestRetainedStorePackageUsesSamePrivacyBoundary(t *testing.T) {
	dir := fixture(t, "package gdgaccounts\nimport \"os\"\nconst secret=\"DO_NOT_EMIT_PRIVATE_STORE_VALUE\"\nfunc Close(){f.Close(); _=os.Getenv(\"GDG_ACCOUNTS_DATA\")}\n")
	r, err := inspectPackage(context.Background(), dir, "gdgaccounts")
	if err != nil || r == nil || len(r.Files) != 1 || r.Files[0].Package != "gdgaccounts" {
		t.Fatal("retained package refused")
	}
	if strings.Contains(encoded(t, r), "DO_NOT_EMIT_PRIVATE_STORE_VALUE") {
		t.Fatal("private literal escaped")
	}
	if _, err := inspectDirectory(context.Background(), dir); err == nil {
		t.Fatal("default package boundary weakened")
	}
}
func TestCallOrderAndLexicalScheduling(t *testing.T) {
	source := `package main
func helper(){ defer cleanup(decode("private argument")); go func(){ storage.Close() }(); listen.Serve() }
`
	r, err := inspectDirectory(context.Background(), fixture(t, source))
	if err != nil {
		t.Fatal("inspection failed")
	}
	calls := r.Files[0].Calls
	if len(calls) != 4 {
		t.Fatal("unexpected call count")
	}
	if calls[0].Callee != "cleanup" || !calls[0].DirectlyDeferred || calls[1].Callee != "decode" || calls[1].DirectlyDeferred || len(calls[1].LexicalContexts) != 1 || calls[1].LexicalContexts[0] != "defer" {
		t.Fatal("defer arguments were misrepresented as deferred calls")
	}
	if calls[2].Callee != "storage.Close" || calls[2].DirectlyGo || calls[2].FunctionLiteralDepth != 1 || len(calls[2].LexicalContexts) != 1 || calls[2].LexicalContexts[0] != "go" {
		t.Fatal("go closure context missing")
	}
	for i := 1; i < len(calls); i++ {
		a, b := calls[i-1], calls[i]
		if a.Line > b.Line || a.Line == b.Line && a.Column > b.Column {
			t.Fatal("calls not in source position order")
		}
	}
}
func TestImportRouteAndEnvironmentValidation(t *testing.T) {
	for _, value := range []string{"../private", "/root/private", "https://private", "example.com/../private", "example.com/private@token", "example.com/secret%2Fvalue"} {
		if validImport(value) {
			t.Fatal("unreviewed import accepted")
		}
	}
	for _, value := range []string{"os", "net/http", "redx/engine", "golang.org/x/crypto/argon2"} {
		if !validImport(value) {
			t.Fatal("valid dependency path refused")
		}
	}
	for _, value := range []string{"/api/accounts/me?token=private", "/api/accounts/privatevalue", "https://example.invalid/api/accounts/me", "/api/accounts/../me", "/api/accounts/%6de"} {
		if routePattern.MatchString(value) {
			t.Fatal("unreviewed route accepted")
		}
	}
	for _, value := range []string{"/api/accounts/me", "POST /v1/accounts/login", "/api/accounts/login/2fa"} {
		if !routePattern.MatchString(value) {
			t.Fatal("reviewed route refused")
		}
	}
	for _, value := range []string{"PASSWORD=value", "private secret", "TOKEN\nPRIVATE", "token"} {
		if envNamePattern.MatchString(value) {
			t.Fatal("invalid environment name accepted")
		}
	}
}
func TestSourceErrorsRevealNoRawText(t *testing.T) {
	r, err := inspectDirectory(context.Background(), fixture(t, "package main\nfunc invalid( SECRET_PRIVATE_PARSE_LITERAL"))
	if err == nil || r != nil || err.Error() != "source_parse_failed" || strings.Contains(err.Error(), "SECRET_PRIVATE") {
		t.Fatal("parse error did not fail privately")
	}
}
func TestBoundsAndCancellation(t *testing.T) {
	dir := fixture(t, "package main\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r, err := inspectDirectory(ctx, dir); err == nil || r != nil || err.Error() != "time_bound_exceeded" {
		t.Fatal("cancelled inspection not refused")
	}
	if err := os.WriteFile(filepath.Join(dir, "large.go"), []byte(strings.Repeat("x", maxFileBytes+1)), 0600); err != nil {
		t.Fatal("fixture create failed")
	}
	if r, err := inspectDirectory(context.Background(), dir); err == nil || r != nil || err.Error() != "source_file_refused" {
		t.Fatal("oversized source not refused")
	}
	dir = t.TempDir()
	for i := 0; i < maxFiles+1; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("part%03d.go", i)), []byte("package main\n"), 0600); err != nil {
			t.Fatal("fixture create failed")
		}
	}
	if r, err := inspectDirectory(context.Background(), dir); err == nil || r != nil || err.Error() != "file_count_bound_exceeded" {
		t.Fatal("file count not bounded")
	}
}
func TestNoRecursiveLocalImport(t *testing.T) {
	dir := fixture(t, "package main\nimport \"redx/private_dependency\"\n")
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0700); err != nil {
		t.Fatal("fixture mkdir failed")
	}
	if err := os.WriteFile(filepath.Join(dir, "nested", "secret.go"), []byte("PRIVATE_NESTED_LITERAL"), 0600); err != nil {
		t.Fatal("fixture create failed")
	}
	r, err := inspectDirectory(context.Background(), dir)
	if err != nil || len(r.Files) != 1 || strings.Contains(encoded(t, r), "PRIVATE_NESTED_LITERAL") {
		t.Fatal("inspector traversed nested dependency")
	}
}
func TestSymlinkSourceRefused(t *testing.T) {
	dir := fixture(t, "package main\n")
	outside := fixture(t, "package main\n")
	link := filepath.Join(dir, "linked.go")
	if err := os.Symlink(filepath.Join(outside, "main.go"), link); err != nil {
		t.Skip("symlink creation unavailable to this test user")
	}
	if r, err := inspectDirectory(context.Background(), dir); err == nil || r != nil || err.Error() != "source_file_refused" {
		t.Fatal("symlinked source not refused")
	}
}
