package games

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"prosvet/internal/netroute"
)

func TestSetHint(t *testing.T) {
	cases := []struct{ in, want string }{
		{"[ONLINE]\r\nDataCenterHint=default\r\n[AUDIO]\r\nX=1\r\n", "[ONLINE]\r\nDataCenterHint=playfab/westeurope\r\n[AUDIO]\r\nX=1\r\n"},
		{"[ONLINE]\nFoo=1\n\n[AUDIO]\nX=1\n", "[ONLINE]\nFoo=1\nDataCenterHint=playfab/westeurope\n\n[AUDIO]\nX=1\n"},
		{"[AUDIO]\nX=1", "[AUDIO]\nX=1\n\n[ONLINE]\nDataCenterHint=playfab/westeurope\n"},
		{"  datacenterhint = playfab/eastus  \n", "DataCenterHint=playfab/westeurope\n"},
	}
	for _, c := range cases {
		if got := string(setHint([]byte(c.in), "playfab/westeurope")); got != c.want {
			t.Errorf("setHint(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSetHintBackupRestore(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "GameSettings.ini")
	orig := "[ONLINE]\r\nDataCenterHint=default\r\n"
	os.WriteFile(p, []byte(orig), 0o644)
	if err := SetHint(p, HintValue("northeurope")); err != nil {
		t.Fatal(err)
	}
	if err := SetHint(p, HintValue("westeurope")); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if h, _ := readHint(b); h != "playfab/westeurope" {
		t.Fatalf("hint %q", h)
	}
	if err := Restore(p); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(p)
	if string(b) != orig {
		t.Fatalf("restore gave %q", b)
	}
}

func TestFindSiegeProton(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := filepath.Join(home, ".local/share/Steam/steamapps/compatdata/359550/pfx/drive_c/users/steamuser/Documents/My Games/Rainbow Six - Siege/abc-123")
	os.MkdirAll(cfg, 0o755)
	os.WriteFile(filepath.Join(cfg, "GameSettings.ini"), []byte("[ONLINE]\nDataCenterHint=playfab/eastus\n"), 0o644)
	got := FindSiege()
	if len(got) != 1 || got[0].Hint != "playfab/eastus" {
		t.Fatalf("%+v", got)
	}
}

func TestPickKeepsCurrent(t *testing.T) {
	ss := []Sample{
		{Region: Region{ID: "westeurope"}, RTT: 40 * time.Millisecond, Sent: 5, Got: 5},
		{Region: Region{ID: "northeurope"}, RTT: 43 * time.Millisecond, Sent: 5, Got: 5},
	}
	if s, _ := Pick(ss, "playfab/northeurope"); s.Region.ID != "northeurope" {
		t.Fatal("switched for 3 ms")
	}
	if s, _ := Pick(ss, "playfab/eastus"); s.Region.ID != "westeurope" {
		t.Fatal("did not pick best")
	}
}

// PROSVET_LIVE=1 go test -run Live -v ./internal/games
func TestLiveMeasure(t *testing.T) {
	if os.Getenv("PROSVET_LIVE") == "" {
		t.Skip("set PROSVET_LIVE=1")
	}
	via := DirectVia(netroute.Detect())
	t.Logf("via %+v", via)
	ss := Measure(context.Background(), append(append([]Region{}, SiegeRegions...), UDPBeacons...), 5, via, nil)
	for _, s := range ss {
		t.Logf("%-18s %-6s rtt=%-8s best=%-8s jitter=%-6s %d/%d %s", s.Region.ID, s.Region.Cloud, Ms(s.RTT), Ms(s.Best), Ms(s.Jitter), s.Got, s.Sent, strings.TrimSpace(s.Err))
	}
}
