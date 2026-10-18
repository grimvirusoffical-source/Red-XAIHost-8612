package engine

import (
	"bytes"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"redx/box"
)

// Checkpoint file == the data file itself (e.g. accounts.red-xai.db): plain
// Box grammar text followed by a comment footer that doubles as the commit
// marker:
//
//	<< REDX-CKPT v1 lsn=42 walgen=3 len=1234 crc32c=deadbeef >>
//
// It is written to <file>.tmp, fsynced, atomically renamed over the data
// file and the directory fsynced, so a crash leaves either the old or the
// new checkpoint, never a mix. WAL segments with gen >= walgen and records
// with lsn > lsn are replayed on top of it.
const footerPrefix = "<< REDX-CKPT v1 "

type ckptMeta struct {
	lsn    uint64
	walGen uint64
}

func writeSnapshotBody(base, body string, meta ckptMeta) error {
	sum := crc32.Checksum([]byte(body), crcTable)
	footer := fmt.Sprintf("%slsn=%d walgen=%d len=%d crc32c=%08x >>\n", footerPrefix, meta.lsn, meta.walGen, len(body), sum)
	tmp := base + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err = f.WriteString(body); err == nil {
		_, err = f.WriteString(footer)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, base); err != nil {
		os.Remove(tmp)
		return err
	}
	syncDir(filepath.Dir(base))
	return nil
}

// loadSnapshot returns ok=false if the file does not exist or is empty.
func loadSnapshot(base string) (root *box.Box, meta ckptMeta, ok bool, err error) {
	data, err := os.ReadFile(base)
	if os.IsNotExist(err) || (err == nil && len(data) == 0) {
		return box.NewBox(), ckptMeta{walGen: 1}, false, nil
	}
	if err != nil {
		return nil, meta, false, err
	}
	i := bytes.LastIndex(data, []byte(footerPrefix))
	if i < 0 {
		return nil, meta, false, fmt.Errorf("%w: %s has no checkpoint footer (not written by RedX, or truncated)", ErrCorrupt, filepath.Base(base))
	}
	line := strings.TrimSpace(string(data[i+len(footerPrefix):]))
	line = strings.TrimSuffix(line, ">>")
	kv := map[string]string{}
	for _, f := range strings.Fields(line) {
		if k, v, ok := strings.Cut(f, "="); ok {
			kv[k] = v
		}
	}
	n, e1 := strconv.Atoi(kv["len"])
	lsn, e2 := strconv.ParseUint(kv["lsn"], 10, 64)
	gen, e3 := strconv.ParseUint(kv["walgen"], 10, 64)
	want, e4 := strconv.ParseUint(kv["crc32c"], 16, 32)
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || n != i {
		return nil, meta, false, fmt.Errorf("%w: %s has a malformed checkpoint footer", ErrCorrupt, filepath.Base(base))
	}
	if got := crc32.Checksum(data[:i], crcTable); got != uint32(want) {
		return nil, meta, false, fmt.Errorf("%w: %s checkpoint checksum mismatch (stored %08x, computed %08x)", ErrCorrupt, filepath.Base(base), want, got)
	}
	root, err = box.ParseDocument(string(data[:i]))
	if err != nil {
		return nil, meta, false, fmt.Errorf("%w: %s: %v", ErrCorrupt, filepath.Base(base), err)
	}
	return root, ckptMeta{lsn: lsn, walGen: gen}, true, nil
}
