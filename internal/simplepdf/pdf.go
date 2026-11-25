// Package simplepdf writes text PDFs with embedded Unicode TrueType fonts.
package simplepdf

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode/utf8"
)

const MaxText = 64 << 10

type font struct {
	data           []byte
	tables         map[string][]byte
	units, metrics int
	used           map[uint16]rune
}

func loadFont(path string) (*font, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, fmt.Errorf("PDF requires DejaVu TrueType font %s: %w", path, e)
	}
	if len(b) < 12 || len(b) > 2<<20 {
		return nil, errors.New("invalid or oversized TrueType font")
	}
	f := &font{data: b, tables: map[string][]byte{}, used: map[uint16]rune{}}
	n := int(binary.BigEndian.Uint16(b[4:6]))
	if 12+n*16 > len(b) {
		return nil, errors.New("invalid TrueType directory")
	}
	for i := 0; i < n; i++ {
		p := 12 + i*16
		off := uint64(binary.BigEndian.Uint32(b[p+8:]))
		size := uint64(binary.BigEndian.Uint32(b[p+12:]))
		if off+size > uint64(len(b)) {
			return nil, errors.New("invalid TrueType table")
		}
		f.tables[string(b[p:p+4])] = b[off : off+size]
	}
	if len(f.tables["head"]) < 54 || len(f.tables["hhea"]) < 36 || len(f.tables["cmap"]) < 4 {
		return nil, errors.New("missing TrueType metrics")
	}
	f.units = int(binary.BigEndian.Uint16(f.tables["head"][18:]))
	f.metrics = int(binary.BigEndian.Uint16(f.tables["hhea"][34:]))
	if f.units == 0 || f.metrics == 0 || len(f.tables["hmtx"]) < 4*f.metrics {
		return nil, errors.New("invalid TrueType metrics")
	}
	return f, nil
}
func (f *font) glyph(r rune) uint16 {
	b := f.tables["cmap"]
	n := int(binary.BigEndian.Uint16(b[2:]))
	if 4+n*8 > len(b) {
		return 0
	}
	for i := 0; i < n; i++ {
		p := 4 + i*8
		platform := binary.BigEndian.Uint16(b[p:])
		enc := binary.BigEndian.Uint16(b[p+2:])
		if platform != 0 && !(platform == 3 && enc == 1) {
			continue
		}
		off := uint64(binary.BigEndian.Uint32(b[p+4:]))
		if off+16 > uint64(len(b)) {
			continue
		}
		t := b[off:]
		if binary.BigEndian.Uint16(t) != 4 {
			continue
		}
		length := int(binary.BigEndian.Uint16(t[2:]))
		if length > len(t) || length < 16 {
			continue
		}
		t = t[:length]
		seg := int(binary.BigEndian.Uint16(t[6:])) / 2
		if 16+8*seg > len(t) {
			continue
		}
		for j := 0; j < seg; j++ {
			end := int(binary.BigEndian.Uint16(t[14+2*j:]))
			start := int(binary.BigEndian.Uint16(t[16+2*seg+2*j:]))
			if int(r) < start || int(r) > end {
				continue
			}
			delta := binary.BigEndian.Uint16(t[16+4*seg+2*j:])
			pos := 16 + 6*seg + 2*j
			ro := int(binary.BigEndian.Uint16(t[pos:]))
			if ro == 0 {
				return uint16(r) + delta
			}
			at := pos + ro + 2*(int(r)-start)
			if at < 0 || at+2 > len(t) {
				return 0
			}
			g := binary.BigEndian.Uint16(t[at:])
			if g != 0 {
				g += delta
			}
			return g
		}
	}
	return 0
}
func (f *font) width(g uint16) int {
	i := min(int(g), f.metrics-1)
	return int(binary.BigEndian.Uint16(f.tables["hmtx"][i*4:])) * 1000 / f.units
}

type objects []string

func (o *objects) add(s string) int { *o = append(*o, s); return len(*o) }
func stream(s []byte, extra string) string {
	var b bytes.Buffer
	w := zlib.NewWriter(&b)
	_, _ = w.Write(s)
	_ = w.Close()
	return fmt.Sprintf("<< /Length %d /Filter /FlateDecode %s >>\nstream\n%s\nendstream", b.Len(), extra, b.String())
}
func (o *objects) addFont(f *font, name string) int {
	file := o.add(stream(f.data, fmt.Sprintf("/Length1 %d", len(f.data))))
	desc := o.add(fmt.Sprintf("<< /Type /FontDescriptor /FontName /%s /Flags 32 /FontBBox [-1100 -500 2200 1600] /ItalicAngle 0 /Ascent 1000 /Descent -300 /CapHeight 750 /StemV 80 /FontFile2 %d 0 R >>", name, file))
	gs := make([]int, 0, len(f.used))
	for g := range f.used {
		gs = append(gs, int(g))
	}
	sort.Ints(gs)
	var widths, cmap strings.Builder
	cmap.WriteString("/CIDInit /ProcSet findresource begin 12 dict begin begincmap /CIDSystemInfo << /Registry (Adobe) /Ordering (UCS) /Supplement 0 >> def /CMapName /MCPUnicode def /CMapType 2 def 1 begincodespacerange <0000> <FFFF> endcodespacerange\n")
	for i := 0; i < len(gs); i += 100 {
		end := min(i+100, len(gs))
		fmt.Fprintf(&cmap, "%d beginbfchar\n", end-i)
		for _, g := range gs[i:end] {
			fmt.Fprintf(&cmap, "<%04X> <%04X>\n", g, f.used[uint16(g)])
		}
		cmap.WriteString("endbfchar\n")
	}
	cmap.WriteString("endcmap CMapName currentdict /CMap defineresource pop end end")
	for _, g := range gs {
		fmt.Fprintf(&widths, "%d [%d] ", g, f.width(uint16(g)))
	}
	unicode := o.add(stream([]byte(cmap.String()), ""))
	cid := o.add(fmt.Sprintf("<< /Type /Font /Subtype /CIDFontType2 /BaseFont /%s /CIDSystemInfo << /Registry (Adobe) /Ordering (Identity) /Supplement 0 >> /FontDescriptor %d 0 R /CIDToGIDMap /Identity /W [%s] >>", name, desc, widths.String()))
	return o.add(fmt.Sprintf("<< /Type /Font /Subtype /Type0 /BaseFont /%s /Encoding /Identity-H /DescendantFonts [%d 0 R] /ToUnicode %d 0 R >>", name, cid, unicode))
}

func Generate(title, text, markdown string) ([]byte, error) {
	return generate(title, text, markdown, "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf", "/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf")
}
func generate(title, text, markdown, sansPath, monoPath string) ([]byte, error) {
	if (text == "") == (markdown == "") || len(title)+len(text)+len(markdown) > MaxText || !utf8.ValidString(title+text+markdown) {
		return nil, errors.New("provide exactly one nonempty UTF-8 text or markdown, at most 64 KiB including title")
	}
	fonts := make([]*font, 2)
	for i, p := range []string{sansPath, monoPath} {
		var e error
		fonts[i], e = loadFont(p)
		if e != nil {
			return nil, e
		}
	}
	var pages []string
	var page strings.Builder
	y := 790
	code := false
	emit := func(line string, fi, size int) error {
		if y < size+40 {
			pages = append(pages, page.String())
			page.Reset()
			y = 790
		}
		if len(pages) >= 128 {
			return errors.New("PDF exceeds 128 pages")
		}
		var hex strings.Builder
		for _, r := range line {
			g := fonts[fi].glyph(r)
			fonts[fi].used[g] = r
			fmt.Fprintf(&hex, "%04X", g)
		}
		fmt.Fprintf(&page, "BT /F%d %d Tf 1 0 0 1 45 %d Tm <%s> Tj ET\n", fi, size, y, hex.String())
		y -= size + 5
		return nil
	}
	lines := strings.Split(strings.ReplaceAll(text+markdown, "\r\n", "\n"), "\n")
	if title != "" {
		lines = append([]string{title, ""}, lines...)
	}
	for index, line := range lines {
		fi, size := 0, 11
		isTitle := title != "" && index == 0
		if !isTitle && markdown != "" && strings.HasPrefix(strings.TrimSpace(line), "```") {
			code = !code
			continue
		}
		if code {
			fi = 1
		}
		if !code && !isTitle && markdown != "" {
			for level := 6; level >= 1; level-- {
				prefix := strings.Repeat("#", level) + " "
				if strings.HasPrefix(line, prefix) {
					line = strings.TrimPrefix(line, prefix)
					size = 22 - 2*level
					break
				}
			}
			if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") {
				line = "• " + line[2:]
			}
		}
		if isTitle {
			size = 20
			fi = 0
		}
		line = strings.ReplaceAll(strings.TrimSuffix(line, "\r"), "\t", "    ")
		runes := []rune(line)
		if len(runes) == 0 {
			y -= size
			continue
		}
		for len(runes) > 0 {
			end, width, lastSpace := 0, 0, 0
			for end < len(runes) {
				r := runes[end]
				if r < 32 || r > 0xffff || fonts[fi].glyph(r) == 0 {
					return nil, fmt.Errorf("unsupported PDF character U+%04X", r)
				}
				next := fonts[fi].width(fonts[fi].glyph(r)) * size
				if width+next > 505000 && end > 0 {
					break
				}
				width += next
				if r == ' ' {
					lastSpace = end
				}
				end++
			}
			if end < len(runes) && !code && lastSpace > 0 {
				end = lastSpace
			}
			if e := emit(string(runes[:end]), fi, size); e != nil {
				return nil, e
			}
			runes = runes[end:]
			if !code {
				for len(runes) > 0 && runes[0] == ' ' {
					runes = runes[1:]
				}
			}
		}
	}
	pages = append(pages, page.String())
	o := objects{"", ""}
	sans := o.addFont(fonts[0], "DejaVuSans")
	mono := o.addFont(fonts[1], "DejaVuSansMono")
	var kids strings.Builder
	for _, p := range pages {
		content := o.add(stream([]byte(p), ""))
		id := o.add(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << /Font << /F0 %d 0 R /F1 %d 0 R >> >> /Contents %d 0 R >>", sans, mono, content))
		fmt.Fprintf(&kids, "%d 0 R ", id)
	}
	o[0] = "<< /Type /Catalog /Pages 2 0 R >>"
	o[1] = fmt.Sprintf("<< /Type /Pages /Count %d /Kids [%s] >>", len(pages), kids.String())
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")
	offsets := []int{0}
	for i, s := range o {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, s)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, off := range offsets[1:] {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	return b.Bytes(), nil
}
