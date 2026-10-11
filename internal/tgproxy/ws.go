package tgproxy

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"prosvet/internal/netroute"
)

// WS is a minimal client-side WebSocket (binary frames only).
type WS struct {
	conn net.Conn
	br   *bufio.Reader
	wmu  sync.Mutex
	born time.Time
	frag []byte
}

// FrontSNI is the SNI used for domain fronting when the real one is cut.
const FrontSNI = "sprinthost.ru"

// DialWS opens wss://domain/apiws via ip. With front=true the TLS SNI is a
// neutral domain and only the chain (not the name) is verified.
func DialWS(ctx context.Context, ip, domain string, front bool, bind netroute.Bind) (*WS, error) {
	d := net.Dialer{Timeout: 6 * time.Second, Control: bind.Control}
	raw, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip, "443"))
	if err != nil {
		return nil, err
	}
	if tc, ok := raw.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
	}
	cfg := &tls.Config{ServerName: domain, NextProtos: []string{"http/1.1"}, MinVersion: tls.VersionTLS12}
	if front {
		cfg.ServerName = FrontSNI
		cfg.InsecureSkipVerify = true
		cfg.VerifyPeerCertificate = func(raws [][]byte, _ [][]*x509.Certificate) error {
			if len(raws) == 0 {
				return errors.New("no certificate")
			}
			certs := make([]*x509.Certificate, 0, len(raws))
			for _, r := range raws {
				c, err := x509.ParseCertificate(r)
				if err != nil {
					return err
				}
				certs = append(certs, c)
			}
			inter := x509.NewCertPool()
			for _, c := range certs[1:] {
				inter.AddCert(c)
			}
			_, err := certs[0].Verify(x509.VerifyOptions{Intermediates: inter})
			return err
		}
	}
	tc := tls.Client(raw, cfg)
	_ = tc.SetDeadline(time.Now().Add(8 * time.Second))
	if err := tc.HandshakeContext(ctx); err != nil {
		raw.Close()
		return nil, err
	}
	key := make([]byte, 16)
	_, _ = rand.Read(key)
	req := "GET /apiws HTTP/1.1\r\nHost: " + domain + "\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + base64.StdEncoding.EncodeToString(key) + "\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Protocol: binary\r\n\r\n"
	if _, err := io.WriteString(tc, req); err != nil {
		tc.Close()
		return nil, err
	}
	br := bufio.NewReaderSize(tc, 64*1024)
	status, err := br.ReadString('\n')
	if err != nil {
		tc.Close()
		return nil, err
	}
	for {
		l, err := br.ReadString('\n')
		if err != nil {
			tc.Close()
			return nil, err
		}
		if strings.TrimSpace(l) == "" {
			break
		}
	}
	if f := strings.Fields(status); len(f) < 2 || f[1] != "101" {
		tc.Close()
		return nil, fmt.Errorf("websocket: %s", strings.TrimSpace(status))
	}
	_ = tc.SetDeadline(time.Time{})
	return &WS{conn: tc, br: br, born: time.Now()}, nil
}

func (w *WS) frame(op byte, p []byte) []byte {
	n := len(p)
	b := []byte{0x80 | op}
	switch {
	case n < 126:
		b = append(b, 0x80|byte(n))
	case n < 65536:
		b = append(b, 0x80|126, byte(n>>8), byte(n))
	default:
		b = append(b, 0x80|127)
		b = binary.BigEndian.AppendUint64(b, uint64(n))
	}
	var m [4]byte
	_, _ = rand.Read(m[:])
	b = append(b, m[:]...)
	start := len(b)
	b = append(b, p...)
	for i := 0; i < n; i++ {
		b[start+i] ^= m[i&3]
	}
	return b
}

// Send writes binary messages (one frame each) in a single write.
func (w *WS) Send(msgs ...[]byte) error {
	var buf []byte
	for _, m := range msgs {
		buf = append(buf, w.frame(2, m)...)
	}
	w.wmu.Lock()
	defer w.wmu.Unlock()
	_, err := w.conn.Write(buf)
	return err
}

// Recv returns the next binary message; io.EOF on close.
func (w *WS) Recv() ([]byte, error) {
	for {
		var h [2]byte
		if _, err := io.ReadFull(w.br, h[:]); err != nil {
			return nil, err
		}
		fin, op := h[0]&0x80 != 0, h[0]&0x0f
		n := uint64(h[1] & 0x7f)
		switch n {
		case 126:
			var e [2]byte
			if _, err := io.ReadFull(w.br, e[:]); err != nil {
				return nil, err
			}
			n = uint64(binary.BigEndian.Uint16(e[:]))
		case 127:
			var e [8]byte
			if _, err := io.ReadFull(w.br, e[:]); err != nil {
				return nil, err
			}
			n = binary.BigEndian.Uint64(e[:])
		}
		if n > 16<<20 {
			return nil, errors.New("websocket: frame too large")
		}
		var mask [4]byte
		masked := h[1]&0x80 != 0
		if masked {
			if _, err := io.ReadFull(w.br, mask[:]); err != nil {
				return nil, err
			}
		}
		p := make([]byte, n)
		if _, err := io.ReadFull(w.br, p); err != nil {
			return nil, err
		}
		if masked {
			for i := range p {
				p[i] ^= mask[i&3]
			}
		}
		switch op {
		case 8:
			w.wmu.Lock()
			_, _ = w.conn.Write(w.frame(8, nil))
			w.wmu.Unlock()
			return nil, io.EOF
		case 9:
			w.wmu.Lock()
			_, _ = w.conn.Write(w.frame(10, p))
			w.wmu.Unlock()
			continue
		case 10:
			continue
		case 0, 1, 2:
			if fin && len(w.frag) == 0 {
				return p, nil
			}
			w.frag = append(w.frag, p...)
			if !fin {
				continue
			}
			m := w.frag
			w.frag = nil
			return m, nil
		}
	}
}

// Close closes the connection.
func (w *WS) Close() error { return w.conn.Close() }
