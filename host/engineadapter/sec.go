package engineadapter

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"redx/box"
	"redx/engine/sec"
	"redx/host"
)

// Access policy. Every database may carry .red-xai-sec files (any "*.red-xai-sec" in its folder
// plus the API-managed "policy.red-xai-sec"). The effective policy for a request is built per
// API key:
//
//	generated grants (from the key's scope and path prefix)  +  the database's .red-xai-sec rules
//
// evaluated by engine/sec (default deny; deny beats allow at equal specificity). The key's own
// scope/db/prefix is additionally checked by the host and is a hard ceiling: a policy file can
// take access away from a key (deny rules, owner rules, role-based denies) but can never give a
// key more than its API scope, database and prefix allow.

const (
	secExt        = ".red-xai-sec"
	secManaged    = "policy" + secExt
	genScopePrefx = "rxkey."
)

type secState struct {
	files []string
	text  string      // combined user policy text ("" = none)
	user  *sec.Policy // parsed user policy (nil = none)

	mu     sync.Mutex
	byName map[string]*sec.Policy
}

func newSecState(files []string, text string, user *sec.Policy) *secState {
	return &secState{files: files, text: text, user: user, byName: map[string]*sec.Policy{}}
}

var prefixSegRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// prefixResources translates a key's path prefix into sec resource patterns that cover the
// same records (never more). Prefixes that name a record id or use wildcards cannot be
// expressed (the policy engine addresses file, box and field only) and are refused here.
func prefixResources(prefix string) ([]string, error) {
	if prefix == "" {
		return []string{"*"}, nil
	}
	bad := func() ([]string, error) {
		return nil, fmt.Errorf("%w: this key's path prefix %q cannot be expressed as a policy resource; SQL, scripts and policies need a prefix of the form file, file:Box or file:Box.Field (no record ids or wildcards)", host.ErrForbidden, prefix)
	}
	file, rest, hasColon := strings.Cut(prefix, ":")
	if !hasColon {
		// a name prefix matches the file itself and anything named "<prefix>.*"
		if !prefixSegRe.MatchString(file) {
			return bad()
		}
		if strings.HasSuffix(file, ".") {
			return []string{file + "*"}, nil
		}
		return []string{file, file + ".*"}, nil
	}
	if file == "" || !prefixSegRe.MatchString(file) {
		return bad()
	}
	if rest == "" {
		return []string{file}, nil
	}
	if strings.ContainsAny(rest, "[]*?/ ") {
		return bad()
	}
	rest = strings.TrimSuffix(rest, ".")
	boxName, field, hasField := strings.Cut(rest, ".")
	if boxName == "" || !prefixSegRe.MatchString(boxName) {
		return bad()
	}
	if !hasField {
		return []string{file + ":" + boxName}, nil
	}
	if field == "" || !prefixSegRe.MatchString(field) {
		return bad()
	}
	return []string{file + ":" + boxName + "." + field, file + ":" + boxName + "." + field + ".*"}, nil
}

func scopeActions(scope string) []string {
	switch scope {
	case host.ScopeAdmin:
		return []string{"read", "write", "delete", "exec"}
	case host.ScopeWrite:
		return []string{"read", "write", "delete"}
	}
	return []string{"read"}
}

func quoteList(ss []string) string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = `"` + s + `"` // every element matched prefixSegRe / is a fixed word: nothing to escape
	}
	return ";[" + strings.Join(q, ",") + "]:"
}

// principalFor builds the sec principal for a key and registers the combined policy under the
// generated scope name the principal carries.
func (st *secState) principalFor(p host.SecPrincipal) (sec.Principal, error) {
	if st == nil {
		return sec.Principal{}, host.ErrUnavailable
	}
	res, err := prefixResources(p.Prefix)
	if err != nil {
		return sec.Principal{}, err
	}
	sum := sha256.Sum256([]byte(p.Prefix))
	name := genScopePrefx + p.Scope + "." + hex.EncodeToString(sum[:4])
	pr := sec.Principal{ID: p.KeyID, Roles: p.Roles, Scopes: []string{name}}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.byName[name] != nil {
		return pr, nil
	}
	gen := fmt.Sprintf("{Scope[2000000001]} = [ {Name[]} = %q, {Actions[]} = %s, {Resource[]} = %s ];\n",
		name, quoteList(scopeActions(p.Scope)), quoteList(res))
	text := gen
	if st.text != "" {
		text = st.text + "\n" + gen
	}
	pol, err := sec.Parse(text)
	if err != nil {
		return sec.Principal{}, fmt.Errorf("combining key grants with the database policy: %w", err)
	}
	if len(st.byName) > 4096 { // bounded: distinct (scope, prefix) pairs are few
		st.byName = map[string]*sec.Policy{}
	}
	st.byName[name] = pol
	return pr, nil
}

// keyAuthz is the authorizer handed to engine/query and engine/code. It finds the combined policy
// of the principal's generated scope; unknown principals are denied.
type keyAuthz struct{ s *store }

func (k keyAuthz) Authorize(pr sec.Principal, act sec.Action, res sec.Resource) sec.Decision {
	st := k.s.secP.Load()
	if st == nil || len(pr.Scopes) == 0 {
		return sec.Decision{Reason: "no policy"}
	}
	st.mu.Lock()
	pol := st.byName[pr.Scopes[0]]
	st.mu.Unlock()
	if pol == nil {
		return sec.Decision{Reason: "unknown principal"}
	}
	return pol.Authorize(pr, act, res)
}

// ---- loading ----

func globFiles(dir, ext string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ext) && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

func (s *store) secText(skipManaged bool) (files []string, text string, err error) {
	names, err := globFiles(s.dir, secExt)
	if err != nil {
		return nil, "", err
	}
	var parts []string
	for _, n := range names {
		if skipManaged && n == secManaged {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.dir, n))
		if err != nil {
			return nil, "", err
		}
		if len(b) > sec.MaxPolicyBytes {
			return nil, "", fmt.Errorf("%s is larger than %d bytes", n, sec.MaxPolicyBytes)
		}
		if strings.TrimSpace(string(b)) == "" {
			continue
		}
		files = append(files, n)
		parts = append(parts, string(b))
	}
	return files, strings.Join(parts, "\n"), nil
}

func checkUserPolicy(text string) (*sec.Policy, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	pol, err := sec.Parse(text)
	if err != nil {
		return nil, err
	}
	for _, sc := range pol.Scopes() {
		if strings.HasPrefix(sc, genScopePrefx) {
			return nil, fmt.Errorf("%w: scope names starting with %q are reserved", sec.ErrBadPolicy, genScopePrefx)
		}
	}
	return pol, nil
}

// loadSec (re)reads every .red-xai-sec file. A policy that does not parse is an error: the
// caller must not fall back to "no policy".
func (s *store) loadSec() error {
	files, text, err := s.secText(false)
	if err != nil {
		return err
	}
	pol, err := checkUserPolicy(text)
	if err != nil {
		return fmt.Errorf("%s: %w", strings.Join(files, ", "), err)
	}
	s.secP.Store(newSecState(files, text, pol))
	if pol != nil && s.cfg.Keyring == nil && s.logf != nil {
		s.logf("sec: %s loaded; no master key is loaded, so packs under Encrypt rules refuse writes and reads", strings.Join(files, ", "))
	}
	return nil
}

// ---- host.Services: policy ----

func (s *store) SecAllow(p host.SecPrincipal, action, file, boxName, field string) bool {
	st := s.secP.Load()
	if st == nil || st.user == nil {
		return true // no policy file: the key ceiling (checked by the host) is the whole story
	}
	pr, err := st.principalFor(p)
	if err != nil {
		return false
	}
	if file == "" {
		return false
	}
	return keyAuthz{s}.Authorize(pr, sec.Action(action), sec.Resource{File: file, Box: boxName, Field: field}).Allowed
}

func (s *store) SecInfo(includeSource bool) (host.SecInfo, error) {
	st := s.secP.Load()
	out := host.SecInfo{Files: []string{}, Roles: []string{}, Scopes: []string{}, MasterKeyIDs: []string{}}
	if st != nil && st.user != nil {
		out.Loaded = true
		out.Files = append(out.Files, st.files...)
		out.Roles = append(out.Roles, st.user.Roles()...)
		out.Scopes = append(out.Scopes, st.user.Scopes()...)
	}
	if s.cfg.Keyring != nil {
		out.MasterKeyIDs = s.cfg.Keyring.IDs() // ids only
	}
	if includeSource {
		if b, err := os.ReadFile(filepath.Join(s.dir, secManaged)); err == nil {
			out.Source = string(b)
		}
	}
	return out, nil
}

func (s *store) SecDefine(src string) error {
	s.secMu.Lock()
	defer s.secMu.Unlock()
	_, others, err := s.secText(true)
	if err != nil {
		return err
	}
	text := others
	if strings.TrimSpace(src) != "" {
		if text != "" {
			text += "\n"
		}
		text += src
	}
	if _, err := checkUserPolicy(text); err != nil {
		return fmt.Errorf("%w: %v", host.ErrInvalid, err)
	}
	p := filepath.Join(s.dir, secManaged)
	if strings.TrimSpace(src) == "" {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	} else if err := writeFileAtomic(p, []byte(src)); err != nil {
		return err
	}
	return s.loadSec()
}

// encryptionFor reports the Encrypt rule that applies to a pack or library file name.
func (s *store) encryptionFor(file string) (keyID string, required bool) {
	st := s.secP.Load()
	if st == nil || st.user == nil {
		return "", false
	}
	return st.user.EncryptionFor(file)
}

var errNoKey = errors.New("policy requires encryption but the master key is not available")

// pathAllowed mirrors engine/query's rule for a field path: the whole path must be allowed and no
// shorter prefix may be explicitly denied (a deny on "Secret" also covers "Secret.Inner").
func pathAllowed(az keyAuthz, pr sec.Principal, act sec.Action, file, boxName string, names []string) bool {
	for i, n := range names {
		d := az.Authorize(pr, act, sec.Resource{File: file, Box: boxName, Field: n})
		if i == len(names)-1 {
			return d.Allowed
		}
		if !d.Allowed && d.Rule != "" {
			return false
		}
	}
	return true
}

func filterBox(b *box.Box, az keyAuthz, pr sec.Principal, file, boxName string, names []string, depth int) {
	for _, e := range b.Entries() {
		cum := e.Key
		if len(names) > 0 {
			cum = names[len(names)-1] + "." + e.Key
		}
		sub := append(append([]string(nil), names...), cum)
		if depth > 64 || !pathAllowed(az, pr, sec.ActionRead, file, boxName, sub) {
			b.Remove(e)
			continue
		}
		if e.Value.Kind == box.KindBox && e.Value.Box != nil {
			filterBox(e.Value.Box, az, pr, file, boxName, sub, depth+1)
		}
	}
}

// SecRead applies the database policy to a value read at path: it refuses a path the caller may
// not read and removes the fields of a record (or box) the caller may not read, so a field-level
// deny holds even when the whole record is requested. No policy file: text is returned unchanged.
func (s *store) SecRead(p host.SecPrincipal, path, text string) (string, error) {
	st := s.secP.Load()
	if st == nil || st.user == nil {
		return text, nil
	}
	pr, err := st.principalFor(p)
	if err != nil {
		return "", err
	}
	bp, err := box.ParsePath(path)
	if err != nil || bp.File == "" || len(bp.Segs) == 0 || bp.HasWild() {
		return "", fmt.Errorf("%w: policy checks need a concrete path with a file", host.ErrInvalid)
	}
	az := keyAuthz{s}
	file, boxName := bp.File, bp.Segs[0].Name
	if !az.Authorize(pr, sec.ActionRead, sec.Resource{File: file, Box: boxName}).Allowed {
		return "", host.ErrForbidden
	}
	var names []string
	for _, sg := range bp.Segs[1:] {
		cum := sg.Name
		if len(names) > 0 {
			cum = names[len(names)-1] + "." + sg.Name
		}
		names = append(names, cum)
	}
	if len(names) > 0 && !pathAllowed(az, pr, sec.ActionRead, file, boxName, names) {
		return "", host.ErrForbidden
	}
	v, err := box.ParseValue(text)
	if err != nil || v.Kind != box.KindBox || v.Box == nil {
		return text, nil
	}
	nb := v.Box.Clone()
	filterBox(nb, az, pr, file, boxName, names, 0)
	v.Box = nb
	return v.String(), nil
}

func checkBoxWrite(b *box.Box, az keyAuthz, pr sec.Principal, file, boxName string, names []string, depth int) bool {
	for _, e := range b.Entries() {
		cum := e.Key
		if len(names) > 0 {
			cum = names[len(names)-1] + "." + e.Key
		}
		sub := append(append([]string(nil), names...), cum)
		if depth > 64 || !pathAllowed(az, pr, sec.ActionWrite, file, boxName, sub) {
			return false
		}
		if e.Value.Kind == box.KindBox && e.Value.Box != nil && !checkBoxWrite(e.Value.Box, az, pr, file, boxName, sub, depth+1) {
			return false
		}
	}
	return true
}

// SecWrite checks a REST write (value text) or delete at path against the policy. A record or box
// value is checked field by field: one denied field refuses the whole write.
func (s *store) SecWrite(p host.SecPrincipal, path, text string, del bool) error {
	st := s.secP.Load()
	if st == nil || st.user == nil {
		return nil
	}
	pr, err := st.principalFor(p)
	if err != nil {
		return err
	}
	bp, err := box.ParsePath(path)
	if err != nil || bp.File == "" || len(bp.Segs) == 0 || bp.HasWild() {
		return fmt.Errorf("%w: policy checks need a concrete path with a file", host.ErrInvalid)
	}
	act := sec.ActionWrite
	if del {
		act = sec.ActionDelete
	}
	az := keyAuthz{s}
	file, boxName := bp.File, bp.Segs[0].Name
	if !az.Authorize(pr, act, sec.Resource{File: file, Box: boxName}).Allowed {
		return host.ErrForbidden
	}
	var names []string
	for _, sg := range bp.Segs[1:] {
		cum := sg.Name
		if len(names) > 0 {
			cum = names[len(names)-1] + "." + sg.Name
		}
		names = append(names, cum)
	}
	if len(names) > 0 && !pathAllowed(az, pr, act, file, boxName, names) {
		return host.ErrForbidden
	}
	if !del {
		if v, err := box.ParseValue(text); err == nil && v.Kind == box.KindBox && v.Box != nil &&
			!checkBoxWrite(v.Box, az, pr, file, boxName, names, 0) {
			return host.ErrForbidden
		}
	}
	return nil
}

func (s *store) SecActive() bool {
	st := s.secP.Load()
	return st != nil && st.user != nil
}
