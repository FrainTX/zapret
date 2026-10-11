package tgproxy

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"testing"
	"time"
)

func TestHandshakeRoundTrip(t *testing.T) {
	secret := NewSecret()
	for _, dc := range []int{2, -4, 203} {
		init, enc, dec := ClientInit(secret, TagPadded, dc)
		cs, err := AcceptInit(init, secret)
		if err != nil {
			t.Fatal(err)
		}
		want := dc
		if want < 0 {
			want = -want
		}
		if cs.DC != want || cs.Media != (dc < 0) || cs.Tag != TagPadded {
			t.Fatalf("dc=%d: got %+v", dc, cs)
		}
		// client -> proxy stream must line up after the init
		msg := []byte("hello mtproto")
		c := append([]byte(nil), msg...)
		enc.XORKeyStream(c, c)
		cs.Dec.XORKeyStream(c, c)
		if !bytes.Equal(c, msg) {
			t.Fatal("client->proxy stream mismatch")
		}
		// proxy -> client
		c = append([]byte(nil), msg...)
		cs.Enc.XORKeyStream(c, c)
		dec.XORKeyStream(c, c)
		if !bytes.Equal(c, msg) {
			t.Fatal("proxy->client stream mismatch")
		}
	}
	if _, err := AcceptInit(make([]byte, 64), secret); err == nil {
		t.Fatal("garbage accepted")
	}
}

// The init we send to Telegram must decode with the standard (secret-less)
// obfuscation, exactly as Telegram's servers do it.
func TestTelegramInit(t *testing.T) {
	ts := NewInit(TagIntermediate, -2)
	kv := ts.Init[8:56]
	s := ctr(kv[:32], kv[32:])
	plain := make([]byte, 64)
	s.XORKeyStream(plain, ts.Init)
	if !bytes.Equal(plain[56:60], TagIntermediate[:]) || int16(binary.LittleEndian.Uint16(plain[60:])) != -2 {
		t.Fatalf("tail = %x", plain[56:64])
	}
}

func TestSplitter(t *testing.T) {
	sp := NewSplitter(TagIntermediate)
	var stream []byte
	for _, n := range []int{8, 40, 4} {
		stream = binary.LittleEndian.AppendUint32(stream, uint32(n))
		stream = append(stream, make([]byte, n)...)
	}
	var got [][]byte
	for i := 0; i < len(stream); i += 7 { // feed in odd chunks
		j := min(i+7, len(stream))
		got = append(got, sp.Push(stream[i:j], stream[i:j])...)
	}
	if len(got) != 3 || len(got[0]) != 12 || len(got[1]) != 44 || len(got[2]) != 8 {
		t.Fatalf("packets: %d", len(got))
	}
	ab := NewSplitter(TagAbridged)
	pk := append([]byte{3}, make([]byte, 12)...)
	if p := ab.Push(pk, pk); len(p) != 1 || len(p[0]) != 13 {
		t.Fatal("abridged split")
	}
}

// TestLive runs the real chain: PROSVET_LIVE=1 go test -run Live ./internal/tgproxy
func TestLive(t *testing.T) {
	if os.Getenv("PROSVET_LIVE") == "" {
		t.Skip("set PROSVET_LIVE=1")
	}
	secret := NewSecret()
	p := New(Config{Listen: "127.0.0.1:0", Secret: secret, Logf: t.Logf, Direct: AroundVPN})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan string)
	go func() {
		p.cfg.Listen = "127.0.0.1:19443"
		ready <- p.cfg.Listen
		_ = p.Serve(ctx)
	}()
	addr := <-ready
	time.Sleep(300 * time.Millisecond)
	for _, dc := range []int{2, 4, 1, 5, -2} {
		tctx, c2 := context.WithTimeout(ctx, 20*time.Second)
		rtt, err := SelfTest(tctx, addr, secret, dc)
		c2()
		t.Logf("DC%d: rtt=%v err=%v", dc, rtt, err)
		if err != nil && (dc == 2 || dc == 4) {
			t.Errorf("DC%d failed: %v", dc, err)
		}
	}
	for k, rs := range p.Routes() {
		for _, r := range rs {
			t.Logf("route %s: %s via %s front=%v if=%q rtt=%v fails=%d", k, r.Domain, r.IP, r.Front, r.Via, r.RTT, r.Fails)
		}
	}
	t.Logf("stats: %+v", p.Snapshot())
}
