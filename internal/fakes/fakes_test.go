package fakes

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// RFC 9001, Appendix A.1.
func TestClientInitialKeysRFC9001(t *testing.T) {
	k := ClientInitialKeys(mustHex(t, "8394c8f03e515708"))
	if want := mustHex(t, "1f369613dd76d5467730efcbe3b1a22d"); !bytes.Equal(k.Key, want) {
		t.Errorf("key = %x, want %x", k.Key, want)
	}
	if want := mustHex(t, "fa044b2f42a3fd3b46fb255c"); !bytes.Equal(k.IV, want) {
		t.Errorf("iv = %x, want %x", k.IV, want)
	}
	if want := mustHex(t, "9f50449e04a0e810283a1e9933adedd2"); !bytes.Equal(k.HP, want) {
		t.Errorf("hp = %x, want %x", k.HP, want)
	}
}

func TestTLSClientHelloRoundTrip(t *testing.T) {
	for _, sni := range []string{"max.ru", "www.google.com", "vk.com", "a-very-long-subdomain.example.co.uk"} {
		for _, pq := range []bool{false, true} {
			rec, err := TLSClientHello(sni, TLSOptions{PostQuantum: pq})
			if err != nil {
				t.Fatal(err)
			}
			got, err := SNIFromTLSRecord(rec)
			if err != nil || got != sni {
				t.Fatalf("SNI(%q, pq=%v) = %q, %v", sni, pq, got, err)
			}
		}
	}
}

func TestTLSClientHelloPadding(t *testing.T) {
	rec, err := TLSClientHello("ya.ru", TLSOptions{PadTo: 517})
	if err != nil {
		t.Fatal(err)
	}
	if len(rec) != 517 {
		t.Fatalf("padded length = %d, want 517", len(rec))
	}
	if s, _ := SNIFromTLSRecord(rec); s != "ya.ru" {
		t.Fatalf("sni after padding = %q", s)
	}
}

func TestQUICInitialRoundTrip(t *testing.T) {
	for _, sni := range []string{"4pda.to", "www.google.com", "steamcommunity.com"} {
		pkt, err := QUICInitial(sni, QUICOptions{Size: 1250})
		if err != nil {
			t.Fatal(err)
		}
		if len(pkt) != 1250 {
			t.Fatalf("len = %d", len(pkt))
		}
		got, err := SNIFromQUICInitial(pkt)
		if err != nil || got != sni {
			t.Fatalf("SNI(%q) = %q, %v", sni, got, err)
		}
	}
}

func TestQUICInitialPostQuantumTooLarge(t *testing.T) {
	if _, err := QUICInitial("max.ru", QUICOptions{Size: 1200, PostQuantum: true}); err != ErrTooLarge {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

// The parser must understand the decoys that ship with zapret and Flowseal,
// which were captured from real browsers.
func TestParseReferenceFakes(t *testing.T) {
	want := map[string]string{
		"quic_initial_www_google_com.bin":    "www.google.com",
		"tls_clienthello_www_google_com.bin": "www.google.com",
		"quic_initial_vk_com.bin":            "st.vk.com",
		"tls_clienthello_vk_com_kyber.bin":   "vk.com",
		"quic_initial_4pda_to.bin":           "4pda.to",
		"tls_clienthello_max_ru.bin":         "www.onetrust.com", // sic: the Flowseal file name lies
		"tls_clienthello_4pda_to.bin":        "4pda.to",
	}
	for name, sni := range want {
		b, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		if got := DescribeSNI(b); got != sni {
			t.Errorf("%s: got %q, want %q", name, got, sni)
		}
	}
}

func TestValidHostname(t *testing.T) {
	for _, ok := range []string{"a.b", "max.ru", "rr1---sn-abc.googlevideo.com", "x_y.z"} {
		if !ValidHostname(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", ".a", "a.", "a..b", "a b", "a/b", "@file", "a;rm -rf"} {
		if ValidHostname(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

func TestSTUN(t *testing.T) {
	b := STUNBindingRequest()
	if len(b)%4 != 0 || b[0] != 0 || b[1] != 1 {
		t.Fatalf("bad stun header: %x", b[:4])
	}
	if int(b[2])<<8|int(b[3]) != len(b)-20 {
		t.Fatalf("bad stun length")
	}
}
