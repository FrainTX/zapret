package fakes

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
)

var errShort = errors.New("fakes: truncated data")

// SNIFromClientHello extracts the server_name from a handshake message
// (type 1). It is the same parsing a DPI box does.
func SNIFromClientHello(h []byte) (string, error) {
	if len(h) < 4 || h[0] != 0x01 {
		return "", errors.New("fakes: not a ClientHello")
	}
	p := h[4:]
	skip := func(n int) bool {
		if len(p) < n {
			return false
		}
		p = p[n:]
		return true
	}
	if !skip(2 + 32) { // version + random
		return "", errShort
	}
	if len(p) < 1 || !skip(1+int(p[0])) { // session id
		return "", errShort
	}
	if len(p) < 2 || !skip(2+int(binary.BigEndian.Uint16(p))) { // cipher suites
		return "", errShort
	}
	if len(p) < 1 || !skip(1+int(p[0])) { // compression
		return "", errShort
	}
	if len(p) < 2 {
		return "", errShort
	}
	extLen := int(binary.BigEndian.Uint16(p))
	p = p[2:]
	if len(p) < extLen {
		return "", errShort
	}
	p = p[:extLen]
	for len(p) >= 4 {
		typ := binary.BigEndian.Uint16(p)
		l := int(binary.BigEndian.Uint16(p[2:]))
		p = p[4:]
		if len(p) < l {
			return "", errShort
		}
		if typ == extServerName {
			e := p[:l]
			if len(e) < 5 || e[2] != 0 {
				return "", errShort
			}
			nl := int(binary.BigEndian.Uint16(e[3:]))
			if len(e) < 5+nl {
				return "", errShort
			}
			return string(e[5 : 5+nl]), nil
		}
		p = p[l:]
	}
	return "", errors.New("fakes: no SNI")
}

// SNIFromTLSRecord parses a TLS handshake record and returns its SNI.
func SNIFromTLSRecord(rec []byte) (string, error) {
	if len(rec) < 5 || rec[0] != 0x16 {
		return "", errors.New("fakes: not a TLS handshake record")
	}
	n := int(binary.BigEndian.Uint16(rec[3:]))
	if len(rec) < 5+n {
		return "", errShort
	}
	return SNIFromClientHello(rec[5 : 5+n])
}

func readVarint(p []byte) (uint64, int, error) {
	if len(p) == 0 {
		return 0, 0, errShort
	}
	n := 1 << (p[0] >> 6)
	if len(p) < n {
		return 0, 0, errShort
	}
	v := uint64(p[0] & 0x3f)
	for i := 1; i < n; i++ {
		v = v<<8 | uint64(p[i])
	}
	return v, n, nil
}

// DecryptInitial removes header protection from a client QUIC v1 Initial and
// returns its decrypted payload (frames).
func DecryptInitial(pkt []byte) ([]byte, error) {
	if len(pkt) < 7 || pkt[0]&0xc0 != 0xc0 || binary.BigEndian.Uint32(pkt[1:]) != 1 {
		return nil, errors.New("fakes: not a QUIC v1 long header")
	}
	if (pkt[0]>>4)&0x03 != 0 {
		return nil, errors.New("fakes: not an Initial packet")
	}
	off := 5
	dcidLen := int(pkt[off])
	off++
	if len(pkt) < off+dcidLen+1 {
		return nil, errShort
	}
	dcid := pkt[off : off+dcidLen]
	off += dcidLen
	off += 1 + int(pkt[off]) // scid
	tokLen, n, err := readVarint(pkt[off:])
	if err != nil {
		return nil, err
	}
	off += n + int(tokLen)
	length, n, err := readVarint(pkt[off:])
	if err != nil {
		return nil, err
	}
	off += n
	pnOffset := off
	if len(pkt) < pnOffset+4+16 || len(pkt) < pnOffset+int(length) {
		return nil, errShort
	}

	keys := ClientInitialKeys(dcid)
	hp, _ := aes.NewCipher(keys.HP)
	mask := make([]byte, 16)
	hp.Encrypt(mask, pkt[pnOffset+4:pnOffset+20])
	hdr := append([]byte(nil), pkt[:pnOffset]...)
	hdr[0] ^= mask[0] & 0x0f
	pnLen := int(hdr[0]&0x03) + 1
	var pn uint64
	for i := 0; i < pnLen; i++ {
		b := pkt[pnOffset+i] ^ mask[1+i]
		hdr = append(hdr, b)
		pn = pn<<8 | uint64(b)
	}
	block, _ := aes.NewCipher(keys.Key)
	aead, _ := cipher.NewGCM(block)
	nonce := make([]byte, 12)
	copy(nonce, keys.IV)
	for i := 0; i < 8; i++ {
		nonce[11-i] ^= byte(pn >> (8 * i))
	}
	ct := pkt[pnOffset+pnLen : pnOffset+int(length)]
	return aead.Open(nil, nonce, ct, hdr)
}

// SNIFromQUICInitial decrypts a client Initial and returns the SNI of the
// ClientHello contained in its CRYPTO frames.
func SNIFromQUICInitial(pkt []byte) (string, error) {
	frames, err := DecryptInitial(pkt)
	if err != nil {
		return "", err
	}
	var crypto []byte
	for len(frames) > 0 {
		switch frames[0] {
		case 0x00: // PADDING
			frames = frames[1:]
		case 0x01: // PING
			frames = frames[1:]
		case 0x06: // CRYPTO
			p := frames[1:]
			offset, n, err := readVarint(p)
			if err != nil {
				return "", err
			}
			p = p[n:]
			l, n, err := readVarint(p)
			if err != nil {
				return "", err
			}
			p = p[n:]
			if uint64(len(p)) < l {
				return "", errShort
			}
			end := int(offset + l)
			if len(crypto) < end {
				crypto = append(crypto, make([]byte, end-len(crypto))...)
			}
			copy(crypto[offset:], p[:l])
			frames = p[l:]
		default:
			return "", errors.New("fakes: unexpected frame in Initial")
		}
	}
	return SNIFromClientHello(crypto)
}

// DescribeSNI tries every supported decoy format and returns the hostname
// embedded in it, or "" if the payload is opaque.
func DescribeSNI(b []byte) string {
	if s, err := SNIFromTLSRecord(b); err == nil {
		return s
	}
	if s, err := SNIFromQUICInitial(b); err == nil {
		return s
	}
	return ""
}
