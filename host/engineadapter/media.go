package engineadapter

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"redx/engine/media"
	"redx/engine/pack"
	"redx/host"
)

// Media libraries: <db>/<lib>.red-xai-media is a pack folder owned by engine/media (sealed
// manifests with sniffed type, size, hash, dimensions/duration, plus the data). It is a
// separate directory from the .red-xai-pack folders, so the pack API cannot reach or alter the
// manifests. Content is sniffed: a declared type that disagrees with the bytes is refused.

const mediaExt = ".red-xai-media"

func (s *store) mediaRoot(lib string) string { return filepath.Join(s.dir, lib+mediaExt) }

func (s *store) mediaFor(lib string, create bool) (*media.Store, error) {
	if err := validSet(lib); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := s.media[lib]; m != nil {
		return m, nil
	}
	root := s.mediaRoot(lib)
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		if !create {
			return nil, host.ErrNotFound
		}
		if err := os.MkdirAll(root, 0o755); err != nil {
			return nil, err
		}
	}
	be := pack.DirBackend{Root: root}
	_, _ = be.RemoveTemp()
	pk, err := pack.New(be, be, pack.Config{})
	if err != nil {
		return nil, extErr(err)
	}
	m, err := media.New(pk, be, media.Config{MaxSize: s.cfg.MaxMediaBytes})
	if err != nil {
		return nil, err
	}
	if s.media == nil {
		s.media = map[string]*media.Store{}
	}
	s.media[lib] = m
	return m, nil
}

func mediaErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, media.ErrNotFound):
		return host.ErrNotFound
	case errors.Is(err, media.ErrInvalidName), errors.Is(err, media.ErrEmpty), errors.Is(err, media.ErrBadRange):
		return fmt.Errorf("%w: %v", host.ErrInvalid, err)
	case errors.Is(err, media.ErrUnsupported), errors.Is(err, media.ErrTypeMismatch):
		return fmt.Errorf("%w: %v", host.ErrMediaType, err)
	case errors.Is(err, media.ErrTooLarge):
		return fmt.Errorf("%w: %v", host.ErrTooLarge, err)
	case errors.Is(err, media.ErrCorrupt):
		return fmt.Errorf("%w: %v", host.ErrCorrupt, err)
	}
	return extErr(err)
}

func mediaInfo(m media.Meta) host.MediaInfo {
	return host.MediaInfo{Name: m.Name, Type: string(m.Type), MIME: m.MIME, Size: m.Size, SHA256: m.SHA256,
		Width: m.Width, Height: m.Height, DurationMS: m.DurationMs, DurationEstimated: m.DurationEstimated}
}

func (s *store) MediaLibs() ([]string, error) {
	names, err := globDirs(s.dir, mediaExt)
	return names, err
}

func globDirs(dir, ext string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, e := range ents {
		if e.IsDir() && strings.HasSuffix(e.Name(), ext) {
			out = append(out, strings.TrimSuffix(e.Name(), ext))
		}
	}
	return out, nil
}

func (s *store) MediaList(lib string) ([]host.MediaInfo, error) {
	m, err := s.mediaFor(lib, false)
	if err != nil {
		return nil, err
	}
	es, err := m.List()
	if err != nil {
		return nil, mediaErr(err)
	}
	out := make([]host.MediaInfo, 0, len(es))
	for _, e := range es {
		if e.Err != nil { // a damaged manifest is listed by name only; fsck reports it
			out = append(out, host.MediaInfo{Name: e.Meta.Name})
			continue
		}
		out = append(out, mediaInfo(e.Meta))
	}
	return out, nil
}

func (s *store) MediaPut(lib, name, declared string, r io.Reader) (host.MediaInfo, error) {
	if s.follower.Load() {
		return host.MediaInfo{}, host.ErrReadOnly
	}
	if _, required := s.encryptionFor(lib + mediaExt); required {
		return host.MediaInfo{}, fmt.Errorf("%w: the .red-xai-sec policy requires encryption for %s%s, and encrypted media libraries are not supported (media is sniffed and range-read in plaintext)", host.ErrUnsupported, lib, mediaExt)
	}
	m, err := s.mediaFor(lib, true)
	if err != nil {
		return host.MediaInfo{}, err
	}
	meta, err := m.Put(name, r, media.PutOptions{Declared: declared})
	if err != nil {
		return host.MediaInfo{}, mediaErr(err)
	}
	return mediaInfo(meta), nil
}

func (s *store) MediaStat(lib, name string) (host.MediaInfo, error) {
	m, err := s.mediaFor(lib, false)
	if err != nil {
		return host.MediaInfo{}, err
	}
	meta, err := m.Stat(name)
	if err != nil {
		return host.MediaInfo{}, mediaErr(err)
	}
	return mediaInfo(meta), nil
}

type mediaFile struct{ *media.Reader }

func (f mediaFile) Info() host.MediaInfo { return mediaInfo(f.Meta()) }

func (s *store) MediaOpen(lib, name string) (host.MediaFile, error) {
	m, err := s.mediaFor(lib, false)
	if err != nil {
		return nil, err
	}
	r, err := m.Open(name)
	if err != nil {
		return nil, mediaErr(err)
	}
	return mediaFile{r}, nil
}

func (s *store) MediaDelete(lib, name string) error {
	if s.follower.Load() {
		return host.ErrReadOnly
	}
	m, err := s.mediaFor(lib, false)
	if err != nil {
		return err
	}
	return mediaErr(m.Delete(name))
}

func (s *store) MediaFsck(lib string, deep bool) (host.MediaFsckResult, error) {
	m, err := s.mediaFor(lib, false)
	if err != nil {
		return host.MediaFsckResult{}, err
	}
	rep, err := m.Fsck(deep)
	if err != nil {
		return host.MediaFsckResult{}, mediaErr(err)
	}
	out := host.MediaFsckResult{OK: rep.OK(), Items: rep.Items, Problems: []string{}, Orphans: append([]string{}, rep.Orphans...)}
	for _, p := range rep.Problems {
		out.Problems = append(out.Problems, fmt.Sprintf("%s: %v", p.Name, p.Err))
	}
	return out, nil
}

func (s *store) MediaSweep(lib string) (int, error) {
	if s.follower.Load() {
		return 0, host.ErrReadOnly
	}
	m, err := s.mediaFor(lib, false)
	if err != nil {
		return 0, err
	}
	n, err := m.Sweep()
	return n, mediaErr(err)
}
