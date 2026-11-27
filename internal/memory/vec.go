package memory

// Meaning layer (0.11.3 item 4). Word vectors come from Natasha navec
// (hudlit_v1_12B_500K_300d_100q, MIT): trained off H96 on 12B words of
// Russian prose. MCPshka keeps the most frequent words in one compact file
// and H96 only looks words up — no model, no inference, no network on read.
// A text vector is the SIF-weighted mean of its word vectors; recall fuses the
// cosine ranking with the BM25 indexes by RRF.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	// NavecURL and NavecSHA256 pin the exact upstream pack.
	NavecURL    = "https://storage.yandexcloud.net/natasha-navec/packs/navec_hudlit_v1_12B_500K_300d_100q.tar"
	NavecSHA256 = "0a89ec8198d0ae9ee3bbb31bbf2c0115bf033e56a337b74d6ca1fe5623165446"
	vecFile     = "memory-vec.bin"
	vecMagic    = "MVEC1\n"
	vecKeep     = 120000 // most frequent words: ~12 MB file
	sifA        = 1e-3
)

// Vectors is the loaded word table: product-quantized rows of sorted words.
type Vectors struct {
	n, dim, qdim, cents int
	codes               []float32 // qdim × cents × (dim/qdim)
	idx                 []byte    // n × qdim
	prob                []float32 // word frequency share for SIF weights
	offs                []uint32  // n+1 offsets into words
	words               []byte
}

// BuildVectors converts the navec tar into the compact table at dir. The pack
// must match NavecSHA256.
func BuildVectors(pack, dir string) (int, error) {
	raw, err := os.ReadFile(pack)
	if err != nil {
		return 0, err
	}
	if sum := sha256.Sum256(raw); hex.EncodeToString(sum[:]) != NavecSHA256 {
		return 0, fmt.Errorf("memory vectors: %s is not the pinned navec pack", pack)
	}
	return buildVectors(raw, dir, vecKeep)
}

// buildVectors parses a navec tar (meta.json, vocab.bin gzip, pq.bin) and
// writes the keep most frequent words.
func buildVectors(raw []byte, dir string, keepN int) (int, error) {
	var vocab, pq []byte
	tr := tar.NewReader(bytes.NewReader(raw))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}
		switch h.Name {
		case "vocab.bin":
			zr, err := gzip.NewReader(tr)
			if err != nil {
				return 0, err
			}
			if vocab, err = io.ReadAll(zr); err != nil {
				return 0, err
			}
		case "pq.bin":
			if pq, err = io.ReadAll(tr); err != nil {
				return 0, err
			}
		}
	}
	le := binary.LittleEndian
	if len(vocab) < 4 || len(pq) < 16 {
		return 0, errors.New("memory vectors: pack lacks vocab or pq")
	}
	n := int(le.Uint32(vocab))
	if len(vocab) < 4+4*n {
		return 0, errors.New("memory vectors: short vocab")
	}
	counts := make([]uint32, n)
	for i := range counts {
		counts[i] = le.Uint32(vocab[4+4*i:])
	}
	words := strings.Split(string(vocab[4+4*n:]), "\n")
	vn, dim, qdim, cents := int(le.Uint32(pq)), int(le.Uint32(pq[4:])), int(le.Uint32(pq[8:])), int(le.Uint32(pq[12:]))
	if vn < n || qdim == 0 || dim%qdim != 0 || len(words) < n || len(pq) < 16+vn*qdim+4*qdim*cents*(dim/qdim) {
		return 0, errors.New("memory vectors: unexpected pq layout")
	}
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool { return counts[order[a]] > counts[order[b]] })
	keep := []int{}
	for _, i := range order {
		if w := words[i]; w != "" && w != "<pad>" && w != "<unk>" && len(keep) < keepN {
			keep = append(keep, i)
		}
	}
	sort.Slice(keep, func(a, b int) bool { return words[keep[a]] < words[keep[b]] })
	var total float64
	for _, i := range keep {
		total += float64(counts[i])
	}
	var out bytes.Buffer
	out.WriteString(vecMagic)
	for _, v := range []int{len(keep), dim, qdim, cents} {
		_ = binary.Write(&out, le, uint32(v))
	}
	out.Write(pq[16+vn*qdim : 16+vn*qdim+4*qdim*cents*(dim/qdim)])
	for _, i := range keep {
		out.Write(pq[16+i*qdim : 16+(i+1)*qdim])
	}
	for _, i := range keep {
		_ = binary.Write(&out, le, float32(float64(counts[i])/total))
	}
	off := uint32(0)
	for _, i := range keep {
		_ = binary.Write(&out, le, off)
		off += uint32(len(words[i]))
	}
	_ = binary.Write(&out, le, off)
	for _, i := range keep {
		out.WriteString(words[i])
	}
	tmp := filepath.Join(dir, vecFile+".tmp")
	if err := os.WriteFile(tmp, out.Bytes(), 0600); err != nil {
		return 0, err
	}
	return len(keep), os.Rename(tmp, filepath.Join(dir, vecFile))
}

// LoadVectors reads the compact table.
func LoadVectors(file string) (*Vectors, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	bad := errors.New("memory vectors: corrupt table")
	if !bytes.HasPrefix(raw, []byte(vecMagic)) || len(raw) < len(vecMagic)+16 {
		return nil, bad
	}
	le := binary.LittleEndian
	p := raw[len(vecMagic):]
	v := &Vectors{n: int(le.Uint32(p)), dim: int(le.Uint32(p[4:])), qdim: int(le.Uint32(p[8:])), cents: int(le.Uint32(p[12:]))}
	p = p[16:]
	if v.qdim == 0 || v.dim%v.qdim != 0 {
		return nil, bad
	}
	nc := v.qdim * v.cents * (v.dim / v.qdim)
	need := 4*nc + v.n*v.qdim + 4*v.n + 4*(v.n+1)
	if len(p) < need {
		return nil, bad
	}
	v.codes = make([]float32, nc)
	for i := range v.codes {
		v.codes[i] = math.Float32frombits(le.Uint32(p[4*i:]))
	}
	p = p[4*nc:]
	v.idx, p = p[:v.n*v.qdim], p[v.n*v.qdim:]
	v.prob = make([]float32, v.n)
	for i := range v.prob {
		v.prob[i] = math.Float32frombits(le.Uint32(p[4*i:]))
	}
	p = p[4*v.n:]
	v.offs = make([]uint32, v.n+1)
	for i := range v.offs {
		v.offs[i] = le.Uint32(p[4*i:])
	}
	v.words = p[4*(v.n+1):]
	if int(v.offs[v.n]) != len(v.words) {
		return nil, bad
	}
	return v, nil
}

func (v *Vectors) word(i int) string { return string(v.words[v.offs[i]:v.offs[i+1]]) }

// find returns the row of a word or, for an unknown inflected form, the
// nearest table word sharing all but its last three letters (a cheap stem).
func (v *Vectors) find(w string) int {
	i := sort.Search(v.n, func(i int) bool { return v.word(i) >= w })
	if i < v.n && v.word(i) == w {
		return i
	}
	r := []rune(w)
	if len(r) < 6 {
		return -1
	}
	stem := string(r[:len(r)-3])
	for _, j := range []int{i, i - 1} {
		if j >= 0 && j < v.n && strings.HasPrefix(v.word(j), stem) {
			return j
		}
	}
	return -1
}

// Embed is the SIF-weighted, normalized mean of the known words of text; nil
// when no word is known.
func (v *Vectors) Embed(text string) []float32 {
	if v == nil {
		return nil
	}
	sub := v.dim / v.qdim
	out := make([]float32, v.dim)
	known := 0
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) }) {
		w = strings.ReplaceAll(w, "ё", "е")
		i := v.find(w)
		if i < 0 {
			continue
		}
		known++
		weight := float32(sifA / (sifA + float64(v.prob[i])))
		row := v.idx[i*v.qdim : (i+1)*v.qdim]
		for q, c := range row {
			code := v.codes[(q*v.cents+int(c))*sub:]
			for k := 0; k < sub; k++ {
				out[q*sub+k] += weight * code[k]
			}
		}
	}
	if known == 0 {
		return nil
	}
	var norm float64
	for _, x := range out {
		norm += float64(x) * float64(x)
	}
	if norm == 0 {
		return nil
	}
	inv := float32(1 / math.Sqrt(norm))
	for i := range out {
		out[i] *= inv
	}
	return out
}

// packVec stores a vector as base64 int8 (cosine ignores the scale).
func packVec(x []float32) string {
	if x == nil {
		return ""
	}
	var max float32
	for _, f := range x {
		max = float32(math.Max(float64(max), math.Abs(float64(f))))
	}
	if max == 0 {
		return ""
	}
	b := make([]byte, len(x))
	for i, f := range x {
		b[i] = byte(int8(math.Round(float64(f / max * 127))))
	}
	return base64.StdEncoding.EncodeToString(b)
}

// cosine of a normalized query and a packed document vector.
func cosine(q []float32, packed string) float64 {
	b, err := base64.StdEncoding.DecodeString(packed)
	if err != nil || len(b) != len(q) {
		return 0
	}
	var dot, norm float64
	for i, c := range b {
		x := float64(int8(c))
		dot += x * float64(q[i])
		norm += x * x
	}
	if norm == 0 {
		return 0
	}
	return dot / math.Sqrt(norm)
}

var vecCache struct {
	sync.Mutex
	file string
	mod  time.Time
	v    *Vectors
}

// vectorsAt returns the cached table of dir, reloading when the file changes;
// nil without a table (recall is then words only).
func vectorsAt(dir string) *Vectors {
	file := filepath.Join(dir, vecFile)
	info, err := os.Stat(file)
	if err != nil {
		return nil
	}
	vecCache.Lock()
	defer vecCache.Unlock()
	if vecCache.file == file && vecCache.mod.Equal(info.ModTime()) {
		return vecCache.v
	}
	v, err := LoadVectors(file)
	if err != nil {
		v = nil
	}
	vecCache.file, vecCache.mod, vecCache.v = file, info.ModTime(), v
	return v
}
