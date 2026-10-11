package fakes

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
)

// quicV1Salt is the RFC 9001 §5.2 initial salt for QUIC version 1.
var quicV1Salt = []byte{
	0x38, 0x76, 0x2c, 0xf7, 0xf5, 0x59, 0x34, 0xb3, 0x4d, 0x17,
	0x9a, 0xe6, 0xa4, 0xc8, 0x0c, 0xad, 0xcc, 0xbb, 0x7f, 0x0a,
}

// InitialKeys holds the client Initial packet protection material.
type InitialKeys struct {
	Key, IV, HP []byte
}

func hkdfExpandLabel(secret []byte, label string, length int) []byte {
	full := "tls13 " + label
	info := make([]byte, 0, 4+len(full))
	info = binary.BigEndian.AppendUint16(info, uint16(length))
	info = append(info, byte(len(full)))
	info = append(info, full...)
	info = append(info, 0) // empty context
	out, err := hkdf.Expand(sha256.New, secret, string(info), length)
	if err != nil {
		panic(err) // only possible with an absurd length
	}
	return out
}

// ClientInitialKeys derives the client Initial keys for a destination
// connection ID, exactly as an on-path observer (and the DPI) would.
func ClientInitialKeys(dcid []byte) InitialKeys {
	initial, err := hkdf.Extract(sha256.New, dcid, quicV1Salt)
	if err != nil {
		panic(err)
	}
	client := hkdfExpandLabel(initial, "client in", 32)
	return InitialKeys{
		Key: hkdfExpandLabel(client, "quic key", 16),
		IV:  hkdfExpandLabel(client, "quic iv", 12),
		HP:  hkdfExpandLabel(client, "quic hp", 16),
	}
}

func appendVarint(b []byte, v uint64) []byte {
	switch {
	case v < 1<<6:
		return append(b, byte(v))
	case v < 1<<14:
		return binary.BigEndian.AppendUint16(b, uint16(v)|0x4000)
	case v < 1<<30:
		return binary.BigEndian.AppendUint32(b, uint32(v)|0x80000000)
	default:
		return binary.BigEndian.AppendUint64(b, v|0xc000000000000000)
	}
}

// quicTransportParams mirrors what Chromium sends, with a zero-length source
// connection id (the same one used in the Initial header).
func quicTransportParams() []byte {
	var b []byte
	param := func(id uint64, val []byte) {
		b = appendVarint(b, id)
		b = appendVarint(b, uint64(len(val)))
		b = append(b, val...)
	}
	vi := func(v uint64) []byte { return appendVarint(nil, v) }
	param(0x01, vi(30000))    // max_idle_timeout
	param(0x03, vi(1472))     // max_udp_payload_size
	param(0x04, vi(15728640)) // initial_max_data
	param(0x05, vi(6291456))  // initial_max_stream_data_bidi_local
	param(0x06, vi(6291456))  // initial_max_stream_data_bidi_remote
	param(0x07, vi(6291456))  // initial_max_stream_data_uni
	param(0x08, vi(100))      // initial_max_streams_bidi
	param(0x09, vi(103))      // initial_max_streams_uni
	param(0x0f, nil)          // initial_source_connection_id (empty)
	param(0x20, vi(65536))    // max_datagram_frame_size
	var g [2]byte
	_, _ = rand.Read(g[:])
	param(0x1b+31*uint64(binary.BigEndian.Uint16(g[:])&0x0fff), nil) // GREASE
	return b
}

// QUICOptions controls a generated QUIC Initial.
type QUICOptions struct {
	// Size of the resulting UDP payload. Clients must send at least 1200.
	Size int
	// PostQuantum adds an ML-KEM key share (the hello then spans 2 packets in
	// real Chrome; here it is truncated into one oversized datagram if Size
	// allows, otherwise rejected).
	PostQuantum bool
	// DCID overrides the random destination connection id (tests only).
	DCID []byte
}

// ErrTooLarge means the ClientHello does not fit into the requested size.
var ErrTooLarge = errors.New("fakes: ClientHello does not fit into the QUIC Initial")

// QUICInitial returns an encrypted QUIC v1 Initial packet whose ClientHello
// carries sni. This is the decoy for --dpi-desync-fake-quic and the "unknown
// UDP" decoys used for games: DPI decrypts it, sees a whitelisted name and
// marks the whole UDP flow as harmless.
func QUICInitial(sni string, o QUICOptions) ([]byte, error) {
	hello, err := clientHelloBody(sni, TLSOptions{QUIC: true, PostQuantum: o.PostQuantum})
	if err != nil {
		return nil, err
	}
	size := o.Size
	if size == 0 {
		size = 1250
	}
	if size < 1200 {
		size = 1200
	}

	dcid := o.DCID
	if dcid == nil {
		dcid = make([]byte, 8)
		_, _ = rand.Read(dcid)
	}
	const pnLen = 1
	var pnByte [1]byte
	_, _ = rand.Read(pnByte[:])
	pn := uint64(pnByte[0] & 0x03)

	// CRYPTO frame.
	frame := []byte{0x06}
	frame = appendVarint(frame, 0)
	frame = appendVarint(frame, uint64(len(hello)))
	frame = append(frame, hello...)

	hdr := []byte{0xc0 | (pnLen - 1)}
	hdr = binary.BigEndian.AppendUint32(hdr, 1)
	hdr = append(hdr, byte(len(dcid)))
	hdr = append(hdr, dcid...)
	hdr = append(hdr, 0) // SCID length 0
	hdr = append(hdr, 0) // token length 0
	// Length is a 2-byte varint; the header is now fixed size.
	headerLen := len(hdr) + 2 + pnLen
	plainLen := size - headerLen - 16
	if plainLen < len(frame) {
		return nil, ErrTooLarge
	}
	plain := make([]byte, plainLen) // trailing zeros are PADDING frames
	copy(plain, frame)

	hdr = binary.BigEndian.AppendUint16(hdr, uint16(pnLen+plainLen+16)|0x4000)
	pnOffset := len(hdr)
	hdr = append(hdr, byte(pn))

	keys := ClientInitialKeys(dcid)
	block, err := aes.NewCipher(keys.Key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, 12)
	copy(nonce, keys.IV)
	for i := 0; i < 8; i++ {
		nonce[11-i] ^= byte(pn >> (8 * i))
	}
	sealed := aead.Seal(nil, nonce, plain, hdr)

	// Header protection: sample starts 4 bytes after the packet number.
	hpBlock, err := aes.NewCipher(keys.HP)
	if err != nil {
		return nil, err
	}
	sample := sealed[4-pnLen : 4-pnLen+16]
	mask := make([]byte, 16)
	hpBlock.Encrypt(mask, sample)
	hdr[0] ^= mask[0] & 0x0f
	for i := 0; i < pnLen; i++ {
		hdr[pnOffset+i] ^= mask[1+i]
	}
	return append(hdr, sealed...), nil
}
