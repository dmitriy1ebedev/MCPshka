package storagefabric

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
)

const frameBytes = 64 << 10
const cipherMagic = "MCPS4ENC1"

func aead(key, salt []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, ErrInvalid
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(salt)
	block, e := aes.NewCipher(mac.Sum(nil))
	if e != nil {
		return nil, e
	}
	return cipher.NewGCM(block)
}

// Format: magic, random per-backup salt, length-prefixed GCM frames.
// A mandatory authenticated empty frame commits the exact end of the stream.
func Encrypt(dst io.Writer, src io.Reader, key []byte) error {
	salt := make([]byte, 32)
	if _, e := rand.Read(salt); e != nil {
		return e
	}
	a, e := aead(key, salt)
	if e != nil {
		return e
	}
	header := append([]byte(cipherMagic), salt...)
	if _, e = dst.Write(header); e != nil {
		return e
	}
	buf := make([]byte, frameBytes)
	var sequence uint64
	for {
		n, readErr := io.ReadFull(src, buf)
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return readErr
		}
		if n > 0 {
			if e = writeFrame(dst, a, header, sequence, buf[:n]); e != nil {
				return e
			}
			sequence++
		}
		if readErr != nil {
			return writeFrame(dst, a, header, sequence, nil)
		}
	}
}
func writeFrame(dst io.Writer, a cipher.AEAD, header []byte, sequence uint64, plain []byte) error {
	nonce := make([]byte, a.NonceSize())
	binary.BigEndian.PutUint64(nonce[len(nonce)-8:], sequence)
	sealed := a.Seal(nil, nonce, plain, header)
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(sealed)))
	if _, e := dst.Write(size[:]); e != nil {
		return e
	}
	_, e := dst.Write(sealed)
	return e
}
func Decrypt(dst io.Writer, src io.Reader, key []byte) error {
	header := make([]byte, len(cipherMagic)+32)
	if _, e := io.ReadFull(src, header); e != nil {
		return e
	}
	if string(header[:len(cipherMagic)]) != cipherMagic {
		return ErrInvalid
	}
	a, e := aead(key, header[len(cipherMagic):])
	if e != nil {
		return e
	}
	var total int64
	for sequence := uint64(0); ; sequence++ {
		var size [4]byte
		if _, e = io.ReadFull(src, size[:]); e != nil {
			return e
		}
		n := binary.BigEndian.Uint32(size[:])
		if n < uint32(a.Overhead()) || n > frameBytes+uint32(a.Overhead()) {
			return ErrInvalid
		}
		sealed := make([]byte, int(n))
		if _, e = io.ReadFull(src, sealed); e != nil {
			return e
		}
		nonce := make([]byte, a.NonceSize())
		binary.BigEndian.PutUint64(nonce[len(nonce)-8:], sequence)
		plain, err := a.Open(nil, nonce, sealed, header)
		if err != nil {
			return errors.New("backup authentication failed")
		}
		if len(plain) == 0 {
			var extra [1]byte
			n, e := src.Read(extra[:])
			if n != 0 || e != io.EOF {
				return errors.New("trailing backup data")
			}
			return nil
		}
		total += int64(len(plain))
		if total > MaxFileBytes {
			return errors.New("decrypted backup exceeds budget")
		}
		if _, e = dst.Write(plain); e != nil {
			return e
		}
	}
}
