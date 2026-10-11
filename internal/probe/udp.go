package probe

import (
	"context"
	"encoding/binary"
	"net"
	"strconv"
	"time"

	"prosvet/internal/fakes"
)

// quicProbe sends a real QUIC v1 Initial carrying the target SNI and waits
// for any datagram back. A server answers a valid Initial with its own
// Initial/Handshake (or Retry), so silence means the flow is filtered.
func quicProbe(ctx context.Context, host string, port int, o Options, r *Result) {
	if port == 0 {
		port = 443
	}
	ips, err := resolve(ctx, host)
	if err != nil {
		r.Class, r.Detail = ClassDNS, shortErr(err)
		return
	}
	conn, err := o.udp(ctx, net.JoinHostPort(ips[0].String(), strconv.Itoa(port)))
	if err != nil {
		r.Class, r.Detail = ClassError, shortErr(err)
		return
	}
	defer conn.Close()
	start := time.Now()
	deadline := start.Add(o.Timeout)
	buf := make([]byte, 2048)
	for attempt := 0; attempt < 3; attempt++ {
		pkt, err := fakes.QUICInitial(host, fakes.QUICOptions{Size: 1250})
		if err != nil {
			r.Class, r.Detail = ClassError, shortErr(err)
			return
		}
		sent := time.Now()
		if _, err := conn.Write(pkt); err != nil {
			r.Class, r.Detail = ClassError, shortErr(err)
			return
		}
		wait := sent.Add(o.Connect / 2)
		if wait.After(deadline) {
			wait = deadline
		}
		_ = conn.SetReadDeadline(wait)
		n, err := conn.Read(buf)
		if err == nil && n > 0 {
			r.Latency = time.Since(sent)
			r.Bytes = int64(n)
			r.Class, r.Detail = ClassOK, "QUIC отвечает"
			return
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			break
		}
	}
	r.Class, r.Detail = ClassNoReply, "сервер не ответил на QUIC Initial"
}

// udpProbe measures UDP round trips to echo beacons (AWS GameLift ping
// beacons on 7770) or STUN servers (Teams/Azure relays on 3478). It is the
// closest stand-in for game traffic that needs no game account.
func udpProbe(ctx context.Context, t Target, host string, o Options, r *Result) {
	ips, err := resolve(ctx, host)
	if err != nil {
		r.Class, r.Detail = ClassDNS, shortErr(err)
		return
	}
	conn, err := o.udp(ctx, net.JoinHostPort(ips[0].String(), strconv.Itoa(t.Port)))
	if err != nil {
		r.Class, r.Detail = ClassError, shortErr(err)
		return
	}
	defer conn.Close()
	deadline := time.Now().Add(o.Timeout)
	buf := make([]byte, 1500)
	var best time.Duration
	got := 0
	const tries = 4
	for i := 0; i < tries; i++ {
		var payload []byte
		if t.Kind == KindSTUN {
			payload = fakes.STUNBindingRequest()
		} else {
			payload = make([]byte, 48)
			copy(payload, "prosvet-ping")
			binary.BigEndian.PutUint64(payload[16:], uint64(time.Now().UnixNano()))
		}
		sent := time.Now()
		if _, err := conn.Write(payload); err != nil {
			break
		}
		wait := sent.Add(o.Connect / 2)
		if wait.After(deadline) {
			wait = deadline
		}
		_ = conn.SetReadDeadline(wait)
		if n, err := conn.Read(buf); err == nil && n > 0 {
			rtt := time.Since(sent)
			if got == 0 || rtt < best {
				best = rtt
			}
			got++
			r.Bytes += int64(n)
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			break
		}
	}
	if got == 0 {
		r.Class, r.Detail = ClassNoReply, "нет ответа по UDP"
		return
	}
	r.Latency = best
	r.Class = ClassOK
	r.Detail = strconv.Itoa(got) + "/" + strconv.Itoa(tries) + " ответов"
}
