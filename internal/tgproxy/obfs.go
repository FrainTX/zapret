// Package tgproxy is a local MTProto proxy for Telegram Desktop that carries
// the traffic to Telegram over WebSocket (the transport of web.telegram.org)
// instead of raw TCP to the data-center addresses, which Russian DPI throttles
// or blocks. It is a Go port of the idea behind Flowseal/tg-ws-proxy (MIT):
// the payload stays end-to-end encrypted by MTProto, the proxy only re-wraps
// the obfuscated2 transport layer.
package tgproxy

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
)

// obfuscated2 layout of the 64-byte init packet.
const (
	initLen    = 64
	skipLen    = 8
	prekeyLen  = 32
	ivLen      = 16
	protoPos   = 56
	dcPos      = 60
	keyIVBytes = prekeyLen + ivLen
)

// Transport tags.
var (
	TagAbridged     = [4]byte{0xef, 0xef, 0xef, 0xef}
	TagIntermediate = [4]byte{0xee, 0xee, 0xee, 0xee}
	TagPadded       = [4]byte{0xdd, 0xdd, 0xdd, 0xdd}
)

func ctr(key, iv []byte) cipher.Stream {
	b, err := aes.NewCipher(key)
	if err != nil {
		panic(err) // key sizes are fixed
	}
	return cipher.NewCTR(b, iv)
}

func reversed(b []byte) []byte {
	out := make([]byte, len(b))
	for i := range b {
		out[len(b)-1-i] = b[i]
	}
	return out
}

func skip(s cipher.Stream, n int) {
	z := make([]byte, n)
	s.XORKeyStream(z, z)
}

// ClientSide holds the streams between the proxy and the Telegram client.
type ClientSide struct {
	Dec   cipher.Stream // client -> proxy
	Enc   cipher.Stream // proxy -> client
	DC    int
	Media bool
	Tag   [4]byte
}

// ErrBadHandshake means the secret is wrong or the client is not MTProto.
var ErrBadHandshake = errors.New("tgproxy: bad handshake")

// AcceptInit parses the client's 64-byte init using the proxy secret.
func AcceptInit(init, secret []byte) (*ClientSide, error) {
	if len(init) != initLen {
		return nil, ErrBadHandshake
	}
	kv := init[skipLen : skipLen+keyIVBytes]
	h := sha256.Sum256(append(append([]byte{}, kv[:prekeyLen]...), secret...))
	dec := ctr(h[:], kv[prekeyLen:])
	plain := make([]byte, initLen)
	dec.XORKeyStream(plain, init)

	var tag [4]byte
	copy(tag[:], plain[protoPos:protoPos+4])
	if tag != TagAbridged && tag != TagIntermediate && tag != TagPadded {
		return nil, ErrBadHandshake
	}
	idx := int16(binary.LittleEndian.Uint16(plain[dcPos:]))
	dc := int(idx)
	media := dc < 0
	if media {
		dc = -dc
	}

	rv := reversed(kv)
	he := sha256.Sum256(append(append([]byte{}, rv[:prekeyLen]...), secret...))
	return &ClientSide{Dec: dec, Enc: ctr(he[:], rv[prekeyLen:]), DC: dc, Media: media, Tag: tag}, nil
}

// TelegramSide holds the streams between the proxy and Telegram.
type TelegramSide struct {
	Init []byte        // 64 bytes to send first
	Enc  cipher.Stream // proxy -> Telegram
	Dec  cipher.Stream // Telegram -> proxy
}

var reservedStarts = [][4]byte{
	{0x48, 0x45, 0x41, 0x44}, {0x50, 0x4f, 0x53, 0x54}, {0x47, 0x45, 0x54, 0x20},
	{0xee, 0xee, 0xee, 0xee}, {0xdd, 0xdd, 0xdd, 0xdd}, {0x16, 0x03, 0x01, 0x02}, {0x4f, 0x50, 0x54, 0x49},
}

// NewInit builds an obfuscated2 init towards Telegram (no secret, the way a
// direct client connects) for a transport tag and signed DC index.
func NewInit(tag [4]byte, dcIdx int) *TelegramSide {
	rnd := make([]byte, initLen)
	for {
		_, _ = rand.Read(rnd)
		if rnd[0] == 0xef {
			continue
		}
		var s [4]byte
		copy(s[:], rnd)
		bad := false
		for _, r := range reservedStarts {
			bad = bad || s == r
		}
		if bad || binary.LittleEndian.Uint32(rnd[4:]) == 0 {
			continue
		}
		break
	}
	kv := rnd[skipLen : skipLen+keyIVBytes]
	enc := ctr(kv[:prekeyLen], kv[prekeyLen:])
	ks := make([]byte, initLen)
	enc.XORKeyStream(ks, make([]byte, initLen)) // keystream for the init itself

	tail := make([]byte, 8)
	copy(tail, tag[:])
	binary.LittleEndian.PutUint16(tail[4:], uint16(int16(dcIdx)))
	_, _ = rand.Read(tail[6:])
	out := append([]byte(nil), rnd...)
	for i := 0; i < 8; i++ {
		out[protoPos+i] = tail[i] ^ ks[protoPos+i]
	}
	rv := reversed(kv)
	return &TelegramSide{Init: out, Enc: enc, Dec: ctr(rv[:prekeyLen], rv[prekeyLen:])}
}

// ClientInit builds a client init *with* a proxy secret — what Telegram
// Desktop sends to us. Used by the self-test.
func ClientInit(secret []byte, tag [4]byte, dcIdx int) (init []byte, enc, dec cipher.Stream) {
	rnd := make([]byte, initLen)
	for {
		_, _ = rand.Read(rnd)
		if rnd[0] != 0xef && binary.LittleEndian.Uint32(rnd[4:]) != 0 {
			break
		}
	}
	kv := rnd[skipLen : skipLen+keyIVBytes]
	h := sha256.Sum256(append(append([]byte{}, kv[:prekeyLen]...), secret...))
	enc = ctr(h[:], kv[prekeyLen:])
	ks := make([]byte, initLen)
	enc.XORKeyStream(ks, make([]byte, initLen))
	tail := make([]byte, 8)
	copy(tail, tag[:])
	binary.LittleEndian.PutUint16(tail[4:], uint16(int16(dcIdx)))
	init = append([]byte(nil), rnd...)
	for i := 0; i < 8; i++ {
		init[protoPos+i] = tail[i] ^ ks[protoPos+i]
	}
	rv := reversed(kv)
	hd := sha256.Sum256(append(append([]byte{}, rv[:prekeyLen]...), secret...))
	return init, enc, ctr(hd[:], rv[prekeyLen:])
}

// Splitter cuts the plaintext transport stream into whole MTProto packets,
// so each goes into its own WebSocket frame (what Telegram's WS gateway
// expects). Ciphertext is carried alongside and cut at the same offsets.
type Splitter struct {
	tag      [4]byte
	cipher   []byte
	plain    []byte
	disabled bool
}

// NewSplitter returns a splitter for a transport tag.
func NewSplitter(tag [4]byte) *Splitter { return &Splitter{tag: tag} }

// Push adds data and returns complete packets (ciphertext).
func (s *Splitter) Push(c, p []byte) [][]byte {
	if s.disabled {
		return [][]byte{c}
	}
	s.cipher = append(s.cipher, c...)
	s.plain = append(s.plain, p...)
	var out [][]byte
	off := 0
	for off < len(s.plain) {
		n, ok := s.next(off)
		if !ok {
			break
		}
		if n <= 0 {
			out = append(out, append([]byte(nil), s.cipher[off:]...))
			off = len(s.plain)
			s.disabled = true
			break
		}
		out = append(out, append([]byte(nil), s.cipher[off:off+n]...))
		off += n
	}
	s.cipher = append(s.cipher[:0], s.cipher[off:]...)
	s.plain = append(s.plain[:0], s.plain[off:]...)
	return out
}

// Flush returns whatever is buffered.
func (s *Splitter) Flush() []byte {
	if len(s.cipher) == 0 {
		return nil
	}
	t := append([]byte(nil), s.cipher...)
	s.cipher, s.plain = s.cipher[:0], s.plain[:0]
	return t
}

func (s *Splitter) next(off int) (int, bool) {
	avail := len(s.plain) - off
	if s.tag == TagAbridged {
		first := s.plain[off]
		hdr, n := 1, int(first&0x7f)*4
		if first == 0x7f || first == 0xff {
			if avail < 4 {
				return 0, false
			}
			n = int(uint32(s.plain[off+1])|uint32(s.plain[off+2])<<8|uint32(s.plain[off+3])<<16) * 4
			hdr = 4
		}
		if n <= 0 {
			return 0, true
		}
		if avail < hdr+n {
			return 0, false
		}
		return hdr + n, true
	}
	if avail < 4 {
		return 0, false
	}
	n := int(binary.LittleEndian.Uint32(s.plain[off:]) & 0x7fffffff)
	if n <= 0 {
		return 0, true
	}
	if avail < 4+n {
		return 0, false
	}
	return 4 + n, true
}
