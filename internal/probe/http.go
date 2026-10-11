package probe

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"

// postSize is large enough to cross the 16-20 KB threshold several times.
const postSize = 64 * 1024

type countingConn struct {
	net.Conn
	read, written int64
}

func (c *countingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.read += int64(n)
	return n, err
}

func (c *countingConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.written += int64(n)
	return n, err
}

func httpsProbe(ctx context.Context, t Target, host string, o Options, r *Result) {
	ips, err := resolve(ctx, host)
	if err != nil {
		r.Class, r.Detail = ClassDNS, shortErr(err)
		return
	}
	port := t.Port
	if port == 0 {
		port = 443
	}
	start := time.Now()
	deadline := start.Add(o.Timeout)
	d := net.Dialer{Timeout: o.Connect, Control: o.Bind.Control}
	raw, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ips[0].String(), strconv.Itoa(port)))
	if err != nil {
		r.Class, r.Detail = ClassIP, shortErr(err)
		return
	}
	defer raw.Close()
	cc := &countingConn{Conn: raw}
	_ = cc.SetDeadline(deadline)

	tc := tls.Client(cc, &tls.Config{ServerName: host, NextProtos: []string{"http/1.1"}, MinVersion: tls.VersionTLS12})
	if err := tc.HandshakeContext(ctx); err != nil {
		var certErr *tls.CertificateVerificationError
		var unknownAuth x509.UnknownAuthorityError
		var hostErr x509.HostnameError
		switch {
		case isLeak(err):
			r.Class = ClassLeak
		case errors.As(err, &certErr), errors.As(err, &unknownAuth), errors.As(err, &hostErr):
			r.Class = ClassTLS
		case isTimeout(err):
			r.Class = ClassDrop
		case isReset(err):
			r.Class = ClassReset
		default:
			r.Class = ClassTLS
		}
		r.Detail = shortErr(err)
		r.Bytes = cc.read
		return
	}

	path := t.Path
	if path == "" {
		path = "/"
	}
	var req strings.Builder
	method := "GET"
	if t.Kind == KindHTTPSPost {
		method = "POST"
	}
	fmt.Fprintf(&req, "%s %s HTTP/1.1\r\nHost: %s\r\nUser-Agent: %s\r\nAccept: */*\r\nAccept-Language: ru-RU,ru;q=0.9,en;q=0.8\r\nAccept-Encoding: identity\r\nConnection: close\r\n", method, path, host, userAgent)
	if method == "POST" {
		fmt.Fprintf(&req, "Content-Type: application/octet-stream\r\nContent-Length: %d\r\n", postSize)
	}
	req.WriteString("\r\n")
	if _, err := io.WriteString(tc, req.String()); err != nil {
		classifyStall(r, err, cc)
		return
	}
	if method == "POST" {
		body := make([]byte, postSize)
		_, _ = rand.Read(body)
		before := cc.written
		if _, err := tc.Write(body); err != nil {
			// Upload stalled: with the 16-20 block the window simply stops
			// opening after a few dozen KB.
			sent := cc.written - before
			if isTimeout(err) && sent > 0 {
				r.Class, r.Detail = ClassFreeze, fmt.Sprintf("отправка встала на %d КБ", sent/1024)
			} else {
				classifyStall(r, err, cc)
			}
			r.Bytes = cc.read
			return
		}
	}

	br := bufio.NewReaderSize(tc, 16*1024)
	status, err := br.ReadString('\n')
	if err != nil {
		if method == "POST" && isTimeout(err) {
			r.Class, r.Detail = ClassFreeze, "сервер не ответил на 64 КБ данных"
			r.Bytes = cc.read
			return
		}
		classifyStall(r, err, cc)
		return
	}
	r.Latency = time.Since(start)
	f := strings.Fields(status)
	if len(f) < 2 || !strings.HasPrefix(f[0], "HTTP/") {
		r.Class, r.Detail = ClassTLS, "не HTTP-ответ"
		return
	}
	code, _ := strconv.Atoi(f[1])
	contentLength := int64(-1)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			classifyStall(r, err, cc)
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		k, v, _ := strings.Cut(line, ":")
		if strings.EqualFold(strings.TrimSpace(k), "Content-Length") {
			contentLength, _ = strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		}
	}
	want := int64(t.MinBytes)
	if want > 0 {
		limit := want
		if contentLength >= 0 && contentLength < limit {
			limit = contentLength // a short complete answer is fine
		}
		_, err := io.CopyN(io.Discard, br, limit)
		// EOF on a response without Content-Length just means it was short.
		if err != nil && !(errors.Is(err, io.EOF) && contentLength < 0) && (isTimeout(err) || isReset(err)) {
			r.Class = ClassFreeze
			r.Detail = fmt.Sprintf("HTTP %d, данные встали на %d КБ", code, cc.read/1024)
			r.Bytes = cc.read
			return
		}
	}
	r.Bytes = cc.read
	r.Class = ClassOK
	r.Detail = "HTTP " + strconv.Itoa(code)
}

func classifyStall(r *Result, err error, cc *countingConn) {
	r.Bytes = cc.read
	switch {
	case isTimeout(err) && cc.read > 4096:
		r.Class = ClassFreeze
		r.Detail = fmt.Sprintf("данные встали на %d КБ", cc.read/1024)
	case isTimeout(err):
		r.Class = ClassDrop
		r.Detail = shortErr(err)
	case isReset(err):
		r.Class = ClassReset
		r.Detail = shortErr(err)
	default:
		r.Class = ClassError
		r.Detail = shortErr(err)
	}
}
