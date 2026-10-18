package engineadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"redx/box"
	"redx/engine/code"
	"redx/engine/sec"
	"redx/host"
)

// Sandboxed scripts (.red-xai-code). A script can only call the host functions registered
// below, only if it declared them in Uses, and only on records that are
//
//  1. inside Config.CodeAllowedBoxes (the sandbox's AllowedBoxes; empty = no record access), AND
//  2. allowed for the running key by the generated key grants + the database's .red-xai-sec rules.
//
// Host functions:
//
//	db_get(record, field)         read    record is "file:Box[ID=n]", field a (dotted) field name; NELL/absent -> nil
//	db_set(record, field, value)  write   value: nil, bool, number, string, array
//	db_delete(record)             delete  returns true when the record existed

const codeExt = ".red-xai-code"

type codeState struct {
	files   map[string]*code.File
	scripts map[string]scriptRef
}

type scriptRef struct {
	file string
	prog *code.Program
}

var fieldRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*(\.[A-Za-z_][A-Za-z0-9_-]*)*$`)

func (s *store) loadCode() {
	st := &codeState{files: map[string]*code.File{}, scripts: map[string]scriptRef{}}
	names, err := globFiles(s.dir, codeExt)
	if err != nil {
		s.codeP.Store(st)
		return
	}
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(s.dir, n))
		if err != nil {
			s.warnf("code: %s: %v", n, err)
			continue
		}
		f, err := code.ParseFile(string(b))
		if err != nil {
			s.warnf("code: %s did not load: %v", n, err)
			continue
		}
		dup := ""
		for _, p := range f.Scripts() {
			if r, ok := st.scripts[p.Name()]; ok {
				dup = fmt.Sprintf("script %q is already defined in %s", p.Name(), r.file)
				break
			}
		}
		if dup != "" {
			s.warnf("code: %s skipped: %s", n, dup)
			continue
		}
		st.files[n] = f
		for _, p := range f.Scripts() {
			st.scripts[p.Name()] = scriptRef{file: n, prog: p}
		}
	}
	s.codeP.Store(st)
}

func globMatch(pat, s string) bool {
	// '*' = any run, '?' = one byte (same rules as sec patterns)
	p, i, star, mark := 0, 0, -1, 0
	for i < len(s) {
		switch {
		case p < len(pat) && pat[p] == '*':
			star, mark = p, i
			p++
		case p < len(pat) && (pat[p] == '?' || pat[p] == s[i]):
			p++
			i++
		case star >= 0:
			p = star + 1
			mark++
			i = mark
		default:
			return false
		}
	}
	for p < len(pat) && pat[p] == '*' {
		p++
	}
	return p == len(pat)
}

// boxAllowed implements the sandbox's AllowedBoxes: patterns "file", "file:Box" (globs).
func (s *store) boxAllowed(file, boxName string) bool {
	for _, pat := range s.cfg.CodeAllowedBoxes {
		f, b, hasBox := strings.Cut(pat, ":")
		if !globMatch(f, file) {
			continue
		}
		if !hasBox || globMatch(b, boxName) {
			return true
		}
	}
	return false
}

func recordArg(args []interface{}, n int) (string, recLoc, error) {
	if len(args) != n {
		return "", recLoc{}, fmt.Errorf("expected %d arguments, got %d", n, len(args))
	}
	t, ok := args[0].(string)
	if !ok {
		return "", recLoc{}, errors.New("the first argument must be a record path string")
	}
	a, ok, _ := pathAddr(t)
	bp, err := box.ParsePath(t)
	if err != nil || !ok || len(bp.Segs) != 1 {
		return "", recLoc{}, fmt.Errorf("%q is not a record path like file.red-xai.db:Box[ID=1]", t)
	}
	return t, recLoc{file: a.File, box: a.Box}, nil
}

type recLoc struct{ file, box string }

func (s *store) codeResource(nargs int, withField bool) func([]interface{}) (sec.Resource, error) {
	return func(args []interface{}) (sec.Resource, error) {
		_, l, err := recordArg(args, nargs)
		if err != nil {
			return sec.Resource{}, err
		}
		if !s.boxAllowed(l.file, l.box) {
			return sec.Resource{}, fmt.Errorf("%s:%s is outside the scripts' allowed boxes", l.file, l.box)
		}
		r := sec.Resource{File: l.file, Box: l.box}
		if withField {
			f, ok := args[1].(string)
			if !ok || !fieldRe.MatchString(f) {
				return sec.Resource{}, errors.New("the field must be a plain (dotted) field name")
			}
			r.Field = f
		}
		return r, nil
	}
}

func plainOf(v box.Value) interface{} {
	switch v.Kind {
	case box.KindNell:
		return nil
	case box.KindString:
		return v.Str
	case box.KindNumber:
		return v.Num
	case box.KindBool:
		return v.Bool
	case box.KindArray:
		out := make([]interface{}, len(v.Arr))
		for i, e := range v.Arr {
			out[i] = plainOf(e)
		}
		return out
	}
	return v.String() // boxes and links come back as their Box text
}

func (s *store) codeHost() (*code.Host, error) {
	s.codeHostOnce.Do(func() {
		s.codeHostV, s.codeHostErr = code.NewHost(
			code.HostFunc{Name: "db_get", Action: sec.ActionRead, Resource: s.codeResource(2, true),
				Fn: func(ctx context.Context, args []interface{}) (interface{}, error) {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
					t, _, _ := recordArg(args, 2)
					txt, err := s.Get(t + "." + args[1].(string))
					if errors.Is(err, host.ErrNotFound) {
						return nil, nil
					}
					if err != nil {
						return nil, err
					}
					v, err := box.ParseValue(txt)
					if err != nil {
						return nil, err
					}
					return plainOf(v), nil
				}},
			code.HostFunc{Name: "db_set", Action: sec.ActionWrite, Resource: s.codeResource(3, true),
				Fn: func(ctx context.Context, args []interface{}) (interface{}, error) {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
					t, _, _ := recordArg(args, 3)
					raw, err := json.Marshal(args[2])
					if err != nil {
						return nil, err
					}
					txt, err := host.JSONToBox(raw)
					if err != nil {
						return nil, err
					}
					if err := s.Set(t+"."+args[1].(string), txt, host.SetOpts{}); err != nil {
						return nil, err
					}
					return true, nil
				}},
			code.HostFunc{Name: "db_delete", Action: sec.ActionDelete, Resource: s.codeResource(1, false),
				Fn: func(ctx context.Context, args []interface{}) (interface{}, error) {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
					t, _, _ := recordArg(args, 1)
					err := s.Delete(t)
					if errors.Is(err, host.ErrNotFound) {
						return false, nil
					}
					return err == nil, err
				}},
		)
	})
	return s.codeHostV, s.codeHostErr
}

func codeErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, sec.ErrDenied), errors.Is(err, code.ErrNotGranted):
		return fmt.Errorf("%w: %v", host.ErrForbidden, err)
	case errors.Is(err, code.ErrLimit), errors.Is(err, code.ErrBadArguments), errors.Is(err, code.ErrBadScript),
		errors.Is(err, code.ErrBadFile), errors.Is(err, code.ErrCanceled), errors.Is(err, code.ErrHostFunc):
		return fmt.Errorf("%w: %v", host.ErrInvalid, err)
	}
	var re *code.RuntimeError
	if errors.As(err, &re) {
		return fmt.Errorf("%w: %v", host.ErrInvalid, err)
	}
	return mapErr(err)
}

func (s *store) CodeList() ([]host.CodeScript, error) {
	st := s.codeP.Load()
	out := []host.CodeScript{}
	if st == nil {
		return out, nil
	}
	for name, r := range st.scripts {
		out = append(out, host.CodeScript{File: r.file, Name: name, Params: r.prog.Params(), Uses: r.prog.Uses()})
	}
	sortScripts(out)
	return out, nil
}

func (s *store) CodeDefine(file, src string) error {
	if err := validSet(file); err != nil {
		return err
	}
	s.codeMu.Lock()
	defer s.codeMu.Unlock()
	p := filepath.Join(s.dir, file+codeExt)
	if strings.TrimSpace(src) == "" {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		s.loadCode()
		return nil
	}
	f, err := code.ParseFile(src)
	if err != nil {
		return fmt.Errorf("%w: %v", host.ErrInvalid, err)
	}
	if st := s.codeP.Load(); st != nil {
		for _, sc := range f.Scripts() {
			if r, ok := st.scripts[sc.Name()]; ok && r.file != file+codeExt {
				return fmt.Errorf("%w: script %q is already defined in %s", host.ErrInvalid, sc.Name(), r.file)
			}
		}
	}
	if err := writeFileAtomic(p, []byte(src)); err != nil {
		return err
	}
	s.loadCode()
	return nil
}

func (s *store) CodeRun(ctx context.Context, p host.SecPrincipal, script string, args []interface{}) (host.CodeResult, error) {
	if s.follower.Load() {
		return host.CodeResult{}, host.ErrReadOnly
	}
	st := s.codeP.Load()
	if st == nil {
		return host.CodeResult{}, host.ErrNotFound
	}
	ref, ok := st.scripts[script]
	if !ok {
		return host.CodeResult{}, host.ErrNotFound
	}
	pr, err := s.secP.Load().principalFor(p)
	if err != nil {
		return host.CodeResult{}, err
	}
	h, err := s.codeHost()
	if err != nil {
		return host.CodeResult{}, err
	}
	res, err := ref.prog.Run(ctx, args, code.RunOptions{Principal: pr, Auth: keyAuthz{s}, Host: h})
	out := host.CodeResult{Value: res.Value, Steps: res.Steps, HostCalls: res.HostCalls, ElapsedMS: float64(res.Elapsed.Microseconds()) / 1000}
	for _, c := range res.Calls {
		out.Calls = append(out.Calls, host.CodeCall{Name: c.Name, Allowed: c.Allowed, Reason: c.Reason})
	}
	return out, codeErr(err)
}

func sortScripts(out []host.CodeScript) {
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
}
