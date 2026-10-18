package code

import (
	"fmt"
	"math"
	"time"

	"redx/box"
)

// MaxFileBytes bounds a .red-xai-code document.
const MaxFileBytes = 4 << 20

// MaxScripts bounds the scripts in one file.
const MaxScripts = 1000

// File is a parsed .red-xai-code document.
type File struct {
	scripts []*Program
	byName  map[string]*Program
}

// Script returns the compiled script with that name.
func (f *File) Script(name string) (*Program, bool) {
	p, ok := f.byName[name]
	return p, ok
}

// Scripts returns every script in document order.
func (f *File) Scripts() []*Program { return append([]*Program(nil), f.scripts...) }

func badFile(format string, a ...interface{}) error {
	return fmt.Errorf("%w: %s", ErrBadFile, fmt.Sprintf(format, a...))
}

// ParseFile parses and compiles a .red-xai-code document:
//
//	{Script[1]} = [
//	    {Name[]}   = "heal",
//	    {Params[]} = ;["target", "amount"]:,
//	    {Uses[]}   = ;["db_get", "db_set"]:,
//	    {Limits[]} = [ {MaxSteps[]} = 5000, {TimeoutMs[]} = 50 ],
//	    {Source[]} = "let hp = db_get(target, \"Health\"); db_set(target, \"Health\", hp + amount); return hp + amount;"
//	];
//
// Name and Source are required; Params, Uses and Limits are optional. Any
// other entry, at either level, is an error (a typo must not silently drop a
// limit). Every script is compiled; the first error is returned with the
// script's name.
func ParseFile(src string) (*File, error) { return ParseFileLimits(src, Limits{}) }

// ParseFileLimits is ParseFile with compile-time limits (MaxSourceBytes,
// MaxNesting) from ceiling.
func ParseFileLimits(src string, ceiling Limits) (*File, error) {
	if len(src) > MaxFileBytes {
		return nil, badFile("document larger than %d bytes", MaxFileBytes)
	}
	root, err := box.ParseDocument(src)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadFile, err)
	}
	f := &File{byName: map[string]*Program{}}
	ents := root.Entries()
	if len(ents) > MaxScripts {
		return nil, badFile("more than %d scripts", MaxScripts)
	}
	for n, e := range ents {
		where := fmt.Sprintf("%s#%d", e.Key, n+1)
		if id, ok := e.ID(); ok {
			where = fmt.Sprintf("%s[ID=%d]", e.Key, id)
		}
		if e.Key != "Script" {
			return nil, badFile("unknown top-level entry %q", e.Key)
		}
		if e.Value.Kind != box.KindBox || e.Value.Box == nil {
			return nil, badFile("%s: must be a box", where)
		}
		p, err := parseScript(e.Value.Box, where, ceiling)
		if err != nil {
			return nil, err
		}
		if f.byName[p.name] != nil {
			return nil, badFile("%s: duplicate script name %q", where, p.name)
		}
		f.byName[p.name] = p
		f.scripts = append(f.scripts, p)
	}
	return f, nil
}

func strList(b *box.Box, key, where string) ([]string, error) {
	e := b.Get(key, 0, false)
	if e == nil {
		return nil, nil
	}
	if e.Value.Kind == box.KindString {
		return []string{e.Value.Str}, nil
	}
	if e.Value.Kind != box.KindArray {
		return nil, badFile("%s: %s must be an array of strings", where, key)
	}
	var out []string
	for _, x := range e.Value.Arr {
		if x.Kind != box.KindString {
			return nil, badFile("%s: %s must contain only strings", where, key)
		}
		out = append(out, x.Str)
	}
	return out, nil
}

func parseScript(b *box.Box, where string, ceiling Limits) (*Program, error) {
	known := map[string]bool{"Name": true, "Params": true, "Uses": true, "Limits": true, "Source": true}
	for _, e := range b.Entries() {
		if !known[e.Key] {
			return nil, badFile("%s: unknown entry %q", where, e.Key)
		}
	}
	ne := b.Get("Name", 0, false)
	if ne == nil || ne.Value.Kind != box.KindString || !validIdent(ne.Value.Str) {
		return nil, badFile("%s: Name must be a string of letters, digits and '_', starting with a letter or '_'", where)
	}
	name := ne.Value.Str
	se := b.Get("Source", 0, false)
	if se == nil || se.Value.Kind != box.KindString {
		return nil, badFile("%s (%s): Source must be a string", where, name)
	}
	params, err := strList(b, "Params", where)
	if err != nil {
		return nil, err
	}
	uses, err := strList(b, "Uses", where)
	if err != nil {
		return nil, err
	}
	var req Limits
	if le := b.Get("Limits", 0, false); le != nil {
		if le.Value.Kind != box.KindBox || le.Value.Box == nil {
			return nil, badFile("%s (%s): Limits must be a box", where, name)
		}
		if req, err = parseLimits(le.Value.Box, where+" ("+name+")"); err != nil {
			return nil, err
		}
	}
	return CompileWith(name, se.Value.Str, params, uses, req, ceiling)
}

func parseLimits(b *box.Box, where string) (Limits, error) {
	var l Limits
	for _, e := range b.Entries() {
		f := e.Value.Num
		if e.Value.Kind != box.KindNumber || f != math.Trunc(f) || f < 1 || f > 1<<40 {
			return l, badFile("%s: Limits.%s must be a positive whole number", where, e.Key)
		}
		n := int64(f)
		switch e.Key {
		case "MaxSteps":
			l.MaxSteps = n
		case "TimeoutMs":
			l.MaxDuration = time.Duration(n) * time.Millisecond
		case "MaxStringLen":
			l.MaxStringLen = int(min64(n, math.MaxInt32))
		case "MaxArrayLen":
			l.MaxArrayLen = int(min64(n, math.MaxInt32))
		case "MaxAlloc":
			l.MaxAlloc = n
		case "MaxDepth":
			l.MaxDepth = int(min64(n, math.MaxInt32))
		case "MaxHostCalls":
			l.MaxHostCalls = int(min64(n, math.MaxInt32))
		default:
			return l, badFile("%s: unknown limit %q", where, e.Key)
		}
	}
	return l, nil
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
