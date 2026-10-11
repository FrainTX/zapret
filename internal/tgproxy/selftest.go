package tgproxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// SelfTest connects to a running proxy like Telegram Desktop would, asks DC
// for a key-exchange nonce (req_pq_multi) and checks Telegram's resPQ reply.
// It proves the whole chain — local proxy, WebSocket route, Telegram — works.
func SelfTest(ctx context.Context, addr string, secret []byte, dc int) (time.Duration, error) {
	d := net.Dialer{Timeout: 5 * time.Second}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return 0, err
	}
	defer c.Close()
	deadline := time.Now().Add(15 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = c.SetDeadline(deadline)

	init, enc, dec := ClientInit(secret, TagIntermediate, dc)
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	var msg bytes.Buffer
	msg.Write(make([]byte, 8)) // auth_key_id = 0
	binary.Write(&msg, binary.LittleEndian, uint64(time.Now().Unix())<<32|uint64(randUint32()&^3))
	binary.Write(&msg, binary.LittleEndian, uint32(20))
	binary.Write(&msg, binary.LittleEndian, uint32(0xbe7e8ef1)) // req_pq_multi
	msg.Write(nonce)
	pkt := binary.LittleEndian.AppendUint32(nil, uint32(msg.Len()))
	pkt = append(pkt, msg.Bytes()...)
	enc.XORKeyStream(pkt, pkt)

	start := time.Now()
	if _, err := c.Write(append(init, pkt...)); err != nil {
		return 0, err
	}
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(c, hdr); err != nil {
		return 0, fmt.Errorf("нет ответа от Telegram: %w", err)
	}
	rtt := time.Since(start)
	dec.XORKeyStream(hdr, hdr)
	n := binary.LittleEndian.Uint32(hdr)
	if n < 40 || n > 4096 {
		return rtt, fmt.Errorf("неожиданная длина ответа %d", n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(c, body); err != nil {
		return rtt, err
	}
	dec.XORKeyStream(body, body)
	if binary.LittleEndian.Uint32(body[20:]) != 0x05162463 { // resPQ
		return rtt, errors.New("ответ не resPQ")
	}
	if !bytes.Equal(body[24:40], nonce) {
		return rtt, errors.New("nonce не совпал")
	}
	return rtt, nil
}

func randUint32() uint32 {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return binary.LittleEndian.Uint32(b[:])
}
