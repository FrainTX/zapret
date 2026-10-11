// Package fakes builds the decoy payloads that the DPI engine injects in front
// of real traffic. Unlike the fixed .bin files shipped with most zapret
// bundles, every decoy here is generated on demand for an arbitrary SNI, so the
// tuner can probe which "whitelisted" hostname a particular ISP lets through.
package fakes

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"strings"
)

// TLSOptions controls the shape of a generated ClientHello.
type TLSOptions struct {
	// ALPN protocols to advertise. Defaults to h2, http/1.1.
	ALPN []string
	// PadTo pads the handshake (via the padding extension) so that the whole
	// record reaches at least this many bytes. 0 disables padding.
	PadTo int
	// QUIC produces a ClientHello suitable for a QUIC Initial: TLS 1.3 only,
	// with quic_transport_parameters and without TLS-over-TCP-only extensions.
	QUIC bool
	// PostQuantum adds an X25519MLKEM768 key share, as Chrome 124+ does. It
	// makes the hello ~1.2KB larger, which spreads it over several segments.
	PostQuantum bool
}

// TLS extension and constant identifiers used below.
const (
	extServerName           = 0x0000
	extStatusRequest        = 0x0005
	extSupportedGroups      = 0x000a
	extECPointFormats       = 0x000b
	extSignatureAlgorithms  = 0x000d
	extALPN                 = 0x0010
	extSCT                  = 0x0012
	extPadding              = 0x0015
	extExtendedMasterSecret = 0x0017
	extCompressCertificate  = 0x001b
	extSessionTicket        = 0x0023
	extSupportedVersions    = 0x002b
	extPSKModes             = 0x002d
	extKeyShare             = 0x0033
	extQUICTransportParams  = 0x0039
	extALPS                 = 0x44cd
	extRenegotiationInfo    = 0xff01

	groupX25519         = 0x001d
	groupSecp256r1      = 0x0017
	groupSecp384r1      = 0x0018
	groupX25519MLKEM768 = 0x11ec
)

var greaseValues = [...]uint16{
	0x0a0a, 0x1a1a, 0x2a2a, 0x3a3a, 0x4a4a, 0x5a5a, 0x6a6a, 0x7a7a,
	0x8a8a, 0x9a9a, 0xaaaa, 0xbaba, 0xcaca, 0xdada, 0xeaea, 0xfafa,
}

// ErrBadHostname is returned for names that cannot be put into an SNI.
var ErrBadHostname = errors.New("fakes: invalid hostname for SNI")

// ValidHostname reports whether s is acceptable as an SNI host name.
func ValidHostname(s string) bool {
	if len(s) == 0 || len(s) > 253 || strings.HasPrefix(s, ".") || strings.HasSuffix(s, ".") {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if len(label) == 0 || len(label) > 63 {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_'
			if !ok {
				return false
			}
		}
	}
	return true
}

type buf struct{ b []byte }

func (w *buf) u8(v byte)      { w.b = append(w.b, v) }
func (w *buf) u16(v uint16)   { w.b = binary.BigEndian.AppendUint16(w.b, v) }
func (w *buf) u24(v int)      { w.b = append(w.b, byte(v>>16), byte(v>>8), byte(v)) }
func (w *buf) raw(p ...byte)  { w.b = append(w.b, p...) }
func (w *buf) str(s string)   { w.b = append(w.b, s...) }
func (w *buf) random(n int)   { p := make([]byte, n); _, _ = rand.Read(p); w.b = append(w.b, p...) }
func (w *buf) mark16() int    { w.b = append(w.b, 0, 0); return len(w.b) }
func (w *buf) close16(at int) { binary.BigEndian.PutUint16(w.b[at-2:], uint16(len(w.b)-at)) }
func (w *buf) mark8() int     { w.b = append(w.b, 0); return len(w.b) }
func (w *buf) close8(at int)  { w.b[at-1] = byte(len(w.b) - at) }
func (w *buf) ext(id uint16, body func(*buf)) {
	w.u16(id)
	m := w.mark16()
	body(w)
	w.close16(m)
}

func pickGrease() (a, b, c uint16) {
	var r [3]byte
	_, _ = rand.Read(r[:])
	a = greaseValues[r[0]%16]
	b = greaseValues[r[1]%16]
	if b == a {
		b = greaseValues[(r[1]+1)%16]
	}
	c = greaseValues[r[2]%16]
	return
}

// clientHelloBody returns the ClientHello handshake message (type 1 + length
// + body), without any record framing.
func clientHelloBody(sni string, o TLSOptions) ([]byte, error) {
	if !ValidHostname(sni) {
		return nil, ErrBadHostname
	}
	alpn := o.ALPN
	if len(alpn) == 0 {
		if o.QUIC {
			alpn = []string{"h3"}
		} else {
			alpn = []string{"h2", "http/1.1"}
		}
	}
	gCipher, gExt1, gGroup := pickGrease()

	var h buf
	h.u8(0x01) // handshake type: client_hello
	h.u24(0)   // length, patched below
	h.u16(0x0303)
	h.random(32) // client random
	if o.QUIC {
		h.u8(0) // QUIC forbids legacy session ids
	} else {
		h.u8(32)
		h.random(32)
	}
	cs := h.mark16()
	if !o.QUIC {
		h.u16(gCipher)
	}
	for _, c := range []uint16{0x1301, 0x1302, 0x1303} {
		h.u16(c)
	}
	if !o.QUIC {
		for _, c := range []uint16{0xc02b, 0xc02f, 0xc02c, 0xc030, 0xcca9, 0xcca8, 0xc013, 0xc014, 0x009c, 0x009d, 0x002f, 0x0035} {
			h.u16(c)
		}
	}
	h.close16(cs)
	h.raw(0x01, 0x00) // compression methods: null

	exts := h.mark16()
	if !o.QUIC {
		h.ext(gExt1, func(*buf) {})
	}
	h.ext(extServerName, func(w *buf) {
		l := w.mark16()
		w.u8(0)
		w.u16(uint16(len(sni)))
		w.str(sni)
		w.close16(l)
	})
	h.ext(extExtendedMasterSecret, func(*buf) {})
	if !o.QUIC {
		h.ext(extRenegotiationInfo, func(w *buf) { w.u8(0) })
	}
	h.ext(extSupportedGroups, func(w *buf) {
		l := w.mark16()
		if !o.QUIC {
			w.u16(gGroup)
		}
		if o.PostQuantum {
			w.u16(groupX25519MLKEM768)
		}
		w.u16(groupX25519)
		w.u16(groupSecp256r1)
		w.u16(groupSecp384r1)
		w.close16(l)
	})
	if !o.QUIC {
		h.ext(extECPointFormats, func(w *buf) { w.raw(0x01, 0x00) })
		h.ext(extSessionTicket, func(*buf) {})
	}
	h.ext(extALPN, func(w *buf) {
		l := w.mark16()
		for _, p := range alpn {
			w.u8(byte(len(p)))
			w.str(p)
		}
		w.close16(l)
	})
	h.ext(extStatusRequest, func(w *buf) { w.raw(0x01, 0x00, 0x00, 0x00, 0x00) })
	h.ext(extSignatureAlgorithms, func(w *buf) {
		l := w.mark16()
		for _, s := range []uint16{0x0403, 0x0804, 0x0401, 0x0503, 0x0805, 0x0501, 0x0806, 0x0601} {
			w.u16(s)
		}
		w.close16(l)
	})
	h.ext(extSCT, func(*buf) {})
	h.ext(extKeyShare, func(w *buf) {
		l := w.mark16()
		if !o.QUIC {
			w.u16(gGroup)
			w.u16(1)
			w.u8(0)
		}
		if o.PostQuantum {
			w.u16(groupX25519MLKEM768)
			w.u16(1216)
			w.random(1216)
		}
		w.u16(groupX25519)
		w.u16(32)
		w.random(32)
		w.close16(l)
	})
	h.ext(extPSKModes, func(w *buf) { w.raw(0x01, 0x01) })
	h.ext(extSupportedVersions, func(w *buf) {
		m := w.mark8()
		if !o.QUIC {
			w.u16(gExt1 ^ 0x1010)
		}
		w.u16(0x0304)
		if !o.QUIC {
			w.u16(0x0303)
		}
		w.close8(m)
	})
	h.ext(extCompressCertificate, func(w *buf) { w.raw(0x02, 0x00, 0x02) }) // brotli
	if !o.QUIC {
		h.ext(extALPS, func(w *buf) {
			l := w.mark16()
			w.u8(2)
			w.str("h2")
			w.close16(l)
		})
	}
	if o.QUIC {
		h.ext(extQUICTransportParams, func(w *buf) { w.raw(quicTransportParams()...) })
	}
	if o.PadTo > 0 {
		// +5 for the record header, +4 for the padding extension header.
		cur := len(h.b) + 5
		if need := o.PadTo - cur - 4; need >= 0 {
			h.ext(extPadding, func(w *buf) { w.raw(make([]byte, need)...) })
		}
	}
	h.close16(exts)

	n := len(h.b) - 4
	h.b[1], h.b[2], h.b[3] = byte(n>>16), byte(n>>8), byte(n)
	return h.b, nil
}

// TLSClientHello returns a complete TLS record carrying a browser-like
// ClientHello for sni. The result is what nfqws/winws expect in
// --dpi-desync-fake-tls and --dpi-desync-split-seqovl-pattern files.
func TLSClientHello(sni string, o TLSOptions) ([]byte, error) {
	o.QUIC = false
	body, err := clientHelloBody(sni, o)
	if err != nil {
		return nil, err
	}
	rec := make([]byte, 0, len(body)+5)
	rec = append(rec, 0x16, 0x03, 0x01)
	rec = binary.BigEndian.AppendUint16(rec, uint16(len(body)))
	return append(rec, body...), nil
}

// HTTPRequest returns a plain HTTP/1.1 request for host, used as the HTTP fake.
func HTTPRequest(host string) ([]byte, error) {
	if !ValidHostname(host) {
		return nil, ErrBadHostname
	}
	return []byte("GET / HTTP/1.1\r\nHost: " + host +
		"\r\nUser-Agent: Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36" +
		"\r\nAccept: text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8\r\nAccept-Encoding: gzip, deflate, br\r\nConnection: keep-alive\r\n\r\n"), nil
}

// STUNBindingRequest returns an RFC 5389 binding request. Some DPI boxes let
// flows that open with STUN through, which helps for voice and game traffic.
func STUNBindingRequest() []byte {
	var w buf
	w.u16(0x0001)
	w.u16(0) // message length, patched below
	w.raw(0x21, 0x12, 0xa4, 0x42)
	w.random(12)
	// SOFTWARE attribute, padded to 4 bytes.
	sw := "libjingle"
	w.u16(0x8022)
	w.u16(uint16(len(sw)))
	w.str(sw)
	for len(w.b)%4 != 0 {
		w.u8(0)
	}
	// The STUN length field excludes the 20 byte header.
	binary.BigEndian.PutUint16(w.b[2:], uint16(len(w.b)-20))
	return w.b
}
