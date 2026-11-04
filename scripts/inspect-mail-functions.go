// Read-only names from a hash-pinned ELF/Go metadata image. Never executes it.
package main

import (
	"bytes"
	"crypto/sha256"
	"debug/elf"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	schema          = "redx-mail-function-metadata/v1"
	maxFileBytes    = 128 << 20
	maxSections     = 512
	maxSectionNames = 64 << 10
	maxSectionName  = 96
	maxPCLNBytes    = 32 << 20
	maxNameTable    = 16 << 20
	maxFunctions    = 100000
	maxFiles        = 200000
	maxSymbolScan   = 512
	maxNameBytes    = 180
	maxNames        = 64
	maxOutputBytes  = 16 << 10
	go120Magic      = 0xfffffff1
)

var safeName = regexp.MustCompile(`^(redx/|main\.)[A-Za-z0-9_./()*$-]+$`)

type result struct {
	SHA256            string   `json:"binary_sha256"`
	Format            string   `json:"metadata_format"`
	Functions         int      `json:"function_records_checked"`
	Names             []string `json:"function_names"`
	Suppressed        int      `json:"unsupported_names_suppressed"`
	StaticOnly        bool     `json:"static_only"`
	RuntimePrecedence bool     `json:"runtime_precedence_established"`
}
type response struct {
	Schema string  `json:"schema"`
	OK     bool    `json:"ok"`
	Result *result `json:"result,omitempty"`
	Error  string  `json:"error,omitempty"`
}
type section struct {
	kind                uint32
	flags, offset, size uint64
	nameOffset          uint32
}

func denied(code string) error { return errors.New(code) }
func span(data []byte, offset, size uint64) ([]byte, error) {
	if offset > uint64(len(data)) || size > uint64(len(data))-offset {
		return nil, denied("metadata_invalid")
	}
	return data[offset : offset+size], nil
}

// Parse only bounded section headers/names. No NewFile, Section.Data,
// decompression, arbitrary string search or ordinary symbol table is used.
func metadataSections(image []byte) ([]byte, uint64, error) {
	if len(image) < 64 || string(image[:4]) != elf.ELFMAG || image[4] != byte(elf.ELFCLASS64) || image[5] != byte(elf.ELFDATA2LSB) || image[6] != 1 || (image[7] != 0 && image[7] != 3) {
		return nil, 0, denied("unsupported_format")
	}
	u16, u32, u64 := binary.LittleEndian.Uint16, binary.LittleEndian.Uint32, binary.LittleEndian.Uint64
	if (u16(image[16:]) != uint16(elf.ET_EXEC) && u16(image[16:]) != uint16(elf.ET_DYN)) || u16(image[18:]) != uint16(elf.EM_X86_64) || u32(image[20:]) != 1 || u16(image[52:]) != 64 {
		return nil, 0, denied("unsupported_format")
	}
	shoff, shentsize, shnum, shstr := u64(image[40:]), u16(image[58:]), u16(image[60:]), u16(image[62:])
	if shentsize != 64 || shnum == 0 || shnum > maxSections || shstr == 0 || shstr >= shnum || shoff < 64 {
		return nil, 0, denied("metadata_limit")
	}
	headers, err := span(image, shoff, uint64(shnum)*64)
	if err != nil {
		return nil, 0, err
	}
	phoff, phentsize, phnum := u64(image[32:]), u16(image[54:]), u16(image[56:])
	if phentsize != 56 || phnum == 0 || phnum > 128 || phoff < 64 {
		return nil, 0, denied("unsupported_format")
	}
	if _, err := span(image, phoff, uint64(phnum)*56); err != nil {
		return nil, 0, err
	}
	read := func(i int) section {
		h := headers[i*64 : (i+1)*64]
		return section{kind: u32(h[4:]), flags: u64(h[8:]), offset: u64(h[24:]), size: u64(h[32:]), nameOffset: u32(h)}
	}
	namesHeader := read(int(shstr))
	if namesHeader.kind != uint32(elf.SHT_STRTAB) || namesHeader.flags&uint64(elf.SHF_COMPRESSED) != 0 || namesHeader.size == 0 || namesHeader.size > maxSectionNames {
		return nil, 0, denied("metadata_limit")
	}
	names, err := span(image, namesHeader.offset, namesHeader.size)
	if err != nil {
		return nil, 0, err
	}
	var pcln []byte
	var textSize uint64
	var textFound bool
	for i := 0; i < int(shnum); i++ {
		s := read(i)
		if s.kind != uint32(elf.SHT_NOBITS) {
			if _, err := span(image, s.offset, s.size); err != nil {
				return nil, 0, err
			}
		}
		if uint64(s.nameOffset) >= uint64(len(names)) {
			return nil, 0, denied("metadata_invalid")
		}
		tail := names[s.nameOffset:]
		if len(tail) > maxSectionName+1 {
			tail = tail[:maxSectionName+1]
		}
		end := bytes.IndexByte(tail, 0)
		if end < 0 {
			return nil, 0, denied("metadata_limit")
		}
		name := string(tail[:end])
		if name != ".text" && name != ".gopclntab" && name != ".data.rel.ro.gopclntab" {
			continue
		}
		if s.kind != uint32(elf.SHT_PROGBITS) || s.flags&uint64(elf.SHF_COMPRESSED) != 0 || s.size == 0 {
			return nil, 0, denied("unsupported_format")
		}
		if name == ".text" {
			if textFound || s.flags&uint64(elf.SHF_EXECINSTR) == 0 || s.size > maxFileBytes {
				return nil, 0, denied("metadata_invalid")
			}
			textFound = true
			textSize = s.size
		} else {
			if pcln != nil || s.size > maxPCLNBytes {
				return nil, 0, denied("metadata_limit")
			}
			pcln, err = span(image, s.offset, s.size)
			if err != nil {
				return nil, 0, err
			}
		}
	}
	if !textFound || pcln == nil {
		return nil, 0, denied("metadata_absent")
	}
	return pcln, textSize, nil
}

// Limited to the 64-bit LE Go 1.20 PCLN layout, retained in local Go 1.27.3
// debug/gosym/pclntab.go:259-269,409-442,462-480. Names only: no PCToLine,
// CU/file/PC-to-line table parsing, unbounded string search or all-Func allocation.
func functionNames(pcln []byte, textSize uint64) ([]string, int, int, error) {
	if len(pcln) < 72 || binary.LittleEndian.Uint32(pcln) != go120Magic || pcln[4] != 0 || pcln[5] != 0 || pcln[6] != 1 || pcln[7] != 8 {
		return nil, 0, 0, denied("unsupported_format")
	}
	u32, u64 := binary.LittleEndian.Uint32, binary.LittleEndian.Uint64
	nfunc, nfile := u64(pcln[8:]), u64(pcln[16:])
	if nfunc == 0 || nfunc > maxFunctions || nfile > maxFiles {
		return nil, 0, 0, denied("metadata_limit")
	}
	offsets := []uint64{u64(pcln[32:]), u64(pcln[40:]), u64(pcln[48:]), u64(pcln[56:]), u64(pcln[64:])}
	previous := uint64(72)
	for _, off := range offsets {
		if off < previous || off > uint64(len(pcln)) {
			return nil, 0, 0, denied("metadata_invalid")
		}
		previous = off
	}
	if offsets[0] == offsets[1] || offsets[1]-offsets[0] > maxNameTable {
		return nil, 0, 0, denied("metadata_limit")
	}
	names := pcln[offsets[0]:offsets[1]]
	data := pcln[offsets[4]:]
	tableBytes := nfunc*8 + 4
	if tableBytes > uint64(len(data)) {
		return nil, 0, 0, denied("metadata_invalid")
	}
	found := map[string]struct{}{}
	suppressed := 0
	for i := uint64(0); i < nfunc; i++ {
		entry, next := uint64(u32(data[i*8:])), uint64(u32(data[(i+1)*8:]))
		record := uint64(u32(data[i*8+4:]))
		if entry > next || next > textSize || record < tableBytes || record > uint64(len(data)) || uint64(len(data))-record < 8 {
			return nil, 0, 0, denied("metadata_invalid")
		}
		if uint64(u32(data[record:])) != entry {
			return nil, 0, 0, denied("metadata_invalid")
		}
		nameOff := int32(u32(data[record+4:]))
		if nameOff < 0 || uint64(nameOff) >= uint64(len(names)) || (nameOff != 0 && names[nameOff-1] != 0) {
			return nil, 0, 0, denied("metadata_invalid")
		}
		tail := names[nameOff:]
		fullRemaining := len(tail)
		if len(tail) > maxSymbolScan {
			tail = tail[:maxSymbolScan]
		}
		end := bytes.IndexByte(tail, 0)
		if end < 0 {
			if fullRemaining < maxSymbolScan {
				return nil, 0, 0, denied("metadata_invalid")
			}
			suppressed++
			continue
		}
		if end == 0 {
			return nil, 0, 0, denied("metadata_invalid")
		}
		name := tail[:end]
		if len(name) > maxNameBytes || !safeName.Match(name) {
			suppressed++
			continue
		}
		s := string(name)
		lower := strings.ToLower(s)
		if !strings.Contains(lower, "mail") && !strings.Contains(lower, "smtp") && !strings.Contains(lower, "relay") {
			continue
		}
		found[s] = struct{}{}
		if len(found) > maxNames {
			return nil, 0, 0, denied("match_limit")
		}
	}
	out := make([]string, 0, len(found))
	for name := range found {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, int(nfunc), suppressed, nil
}

func inspect(image []byte, expected []byte) (*result, error) {
	if len(image) > maxFileBytes {
		return nil, denied("input_limit")
	}
	sum := sha256.Sum256(image)
	if len(expected) != sha256.Size || !bytes.Equal(sum[:], expected) {
		return nil, denied("hash_mismatch")
	}
	pcln, textSize, err := metadataSections(image)
	if err != nil {
		return nil, err
	}
	names, n, suppressed, err := functionNames(pcln, textSize)
	if err != nil {
		return nil, err
	}
	return &result{SHA256: hex.EncodeToString(sum[:]), Format: "elf64-le-amd64/go120-pclntab", Functions: n, Names: names, Suppressed: suppressed, StaticOnly: true}, nil
}

func execute(args []string) (out response) {
	out = response{Schema: schema}
	defer func() {
		if recover() != nil {
			out = response{Schema: schema, Error: "metadata_invalid"}
		}
	}()
	if len(args) != 4 || args[0] != "--binary" || args[2] != "--expected-sha256" || !filepath.IsAbs(args[1]) || strings.HasPrefix(args[1], "\\\\") || strings.HasPrefix(args[1], "//") || len(args[1]) > 4096 || strings.ContainsAny(args[1], "\x00\r\n") || len(args[3]) != 64 {
		out.Error = "arguments_invalid"
		return
	}
	expected, err := hex.DecodeString(args[3])
	if err != nil || len(expected) != sha256.Size {
		out.Error = "arguments_invalid"
		return
	}
	selected, err := os.Stat(args[1])
	if err != nil || !selected.Mode().IsRegular() {
		out.Error = "input_refused"
		return
	}
	if selected.Size() < 0 || selected.Size() > maxFileBytes {
		out.Error = "input_limit"
		return
	}
	f, err := os.Open(args[1])
	if err != nil {
		out.Error = "input_refused"
		return
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil || !before.Mode().IsRegular() || !os.SameFile(selected, before) {
		out.Error = "input_refused"
		return
	}
	if before.Size() < 0 || before.Size() > maxFileBytes {
		out.Error = "input_limit"
		return
	}
	image, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		out.Error = "input_refused"
		return
	}
	after, err := f.Stat()
	if err != nil || len(image) > maxFileBytes || int64(len(image)) != before.Size() || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		out.Error = "input_changed_or_limit"
		return
	}
	out.Result, err = inspect(image, expected)
	if err != nil {
		out.Error = err.Error()
		return
	}
	out.OK = true
	return
}

func run(args []string, output io.Writer) int {
	out := execute(args)
	encoded, err := json.Marshal(out)
	if err != nil || len(encoded)+1 > maxOutputBytes {
		out = response{Schema: schema, Error: "output_limit"}
		encoded, _ = json.Marshal(out)
	}
	if _, err := output.Write(append(encoded, '\n')); err != nil {
		return 1
	}
	if !out.OK {
		return 1
	}
	return 0
}
func main() { os.Exit(run(os.Args[1:], os.Stdout)) }
