// Package games measures the latency to game data centres and pins the
// nearest one in game settings, so the game never settles on a far region
// just because the near one answered its first ping late.
package games

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"slices"
	"sort"
	"strconv"
	"sync"
	"syscall"
	"time"

	"prosvet/internal/netroute"
)

// Region is a data centre a game can be pinned to.
type Region struct {
	ID    string // DataCenterHint value without the "playfab/" prefix, or AWS id
	Title string
	Cloud string // "azure" or "aws"
	Host  string // endpoint in that region
	Port  int
	UDP   bool // UDP echo (GameLift beacon) instead of a TCP handshake
}

// SiegeRegions are the PlayFab (Azure) regions Rainbow Six Siege offers in
// GameSettings.ini. Each is measured with a TCP handshake to a regional
// Azure front end: a SYN/SYN-ACK round trip is as close to the game's own
// QoS ping as one gets without a game account, and TSPU does not touch it.
var SiegeRegions = []Region{
	{ID: "westeurope", Title: "Западная Европа · Амстердам"},
	{ID: "northeurope", Title: "Северная Европа · Дублин"},
	{ID: "uaenorth", Title: "ОАЭ · Дубай"},
	{ID: "eastus", Title: "США восток · Вирджиния"},
	{ID: "centralus", Title: "США центр · Айова"},
	{ID: "southcentralus", Title: "США юг · Техас"},
	{ID: "westus", Title: "США запад · Калифорния"},
	{ID: "brazilsouth", Title: "Бразилия · Сан-Паулу"},
	{ID: "southafricanorth", Title: "ЮАР · Йоханнесбург"},
	{ID: "eastasia", Title: "Восточная Азия · Гонконг"},
	{ID: "southeastasia", Title: "Юго-Восточная Азия · Сингапур"},
	{ID: "japaneast", Title: "Япония · Токио"},
	{ID: "australiaeast", Title: "Австралия · Сидней"},
}

// UDPBeacons are AWS GameLift echo beacons. They show whether game-like
// UDP goes through the bypass at all and how much it costs.
var UDPBeacons = []Region{
	{ID: "eu-central-1", Title: "AWS Франкфурт", Cloud: "aws", Host: "gamelift-ping.eu-central-1.api.aws", Port: 7770, UDP: true},
	{ID: "eu-north-1", Title: "AWS Стокгольм", Cloud: "aws", Host: "gamelift-ping.eu-north-1.api.aws", Port: 7770, UDP: true},
	{ID: "eu-west-1", Title: "AWS Ирландия", Cloud: "aws", Host: "gamelift-ping.eu-west-1.api.aws", Port: 7770, UDP: true},
	{ID: "eu-west-2", Title: "AWS Лондон", Cloud: "aws", Host: "gamelift-ping.eu-west-2.api.aws", Port: 7770, UDP: true},
	{ID: "me-south-1", Title: "AWS Бахрейн", Cloud: "aws", Host: "gamelift-ping.me-south-1.api.aws", Port: 7770, UDP: true},
}

func init() {
	for i := range SiegeRegions {
		r := &SiegeRegions[i]
		r.Cloud, r.Host, r.Port = "azure", r.ID+".api.cognitive.microsoft.com", 443
	}
}

// Sample is the measurement of one region.
type Sample struct {
	Region Region        `json:"region"`
	RTT    time.Duration `json:"rtt"`    // median
	Best   time.Duration `json:"best"`   // minimum
	Jitter time.Duration `json:"jitter"` // mean absolute deviation from the median
	Sent   int           `json:"sent"`
	Got    int           `json:"got"`
	Err    string        `json:"err,omitempty"`
}

// Loss is the share of lost probes.
func (s Sample) Loss() float64 {
	if s.Sent == 0 {
		return 1
	}
	return 1 - float64(s.Got)/float64(s.Sent)
}

// OK reports whether the region answered most probes.
func (s Sample) OK() bool { return s.Got > 0 && s.Loss() <= 0.34 }

// Score orders regions: median plus jitter penalty plus a heavy loss
// penalty, which is what a player actually feels.
func (s Sample) Score() time.Duration {
	if s.Got == 0 {
		return time.Hour
	}
	return s.RTT + 2*s.Jitter + time.Duration(s.Loss()*float64(300*time.Millisecond))
}

// Via pins probes to a network interface (see netroute.Bind).
type Via = netroute.Bind

// Measure probes every region n times (regions in parallel, probes within
// a region spaced apart so they do not queue behind each other) and
// returns them best first. progress, if set, is called as regions finish.
func Measure(ctx context.Context, regions []Region, n int, via Via, progress func(Sample)) []Sample {
	if n <= 0 {
		n = 7
	}
	out := make([]Sample, len(regions))
	var wg sync.WaitGroup
	var mu sync.Mutex
	sem := make(chan struct{}, 8)
	for i, r := range regions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			s := measureOne(ctx, r, n, via)
			out[i] = s
			if progress != nil {
				mu.Lock()
				progress(s)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	Rank(out)
	return out
}

// Rank sorts samples best first.
func Rank(ss []Sample) {
	sort.SliceStable(ss, func(i, j int) bool { return ss[i].Score() < ss[j].Score() })
}

func measureOne(ctx context.Context, r Region, n int, via Via) Sample {
	s := Sample{Region: r}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", r.Host)
	if err != nil || len(addrs) == 0 {
		s.Err = "не резолвится"
		return s
	}
	addr := net.JoinHostPort(addrs[0].String(), strconv.Itoa(r.Port))
	var rtts []time.Duration
	for i := 0; i < n && ctx.Err() == nil; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
			case <-time.After(120 * time.Millisecond):
			}
		}
		s.Sent++
		var d time.Duration
		var err error
		if r.UDP {
			d, err = udpEcho(ctx, addr, via)
		} else {
			d, err = tcpHandshake(ctx, addr, via)
		}
		if err != nil {
			s.Err = shortErr(err)
			continue
		}
		rtts = append(rtts, d)
	}
	s.Got = len(rtts)
	if s.Got == 0 {
		return s
	}
	s.Err = ""
	slices.Sort(rtts)
	s.Best, s.RTT = rtts[0], rtts[len(rtts)/2]
	var dev time.Duration
	for _, d := range rtts {
		dev += (d - s.RTT).Abs()
	}
	s.Jitter = dev / time.Duration(len(rtts))
	return s
}

func tcpHandshake(ctx context.Context, addr string, via Via) (time.Duration, error) {
	d := net.Dialer{Timeout: 1500 * time.Millisecond, Control: via.Control}
	start := time.Now()
	c, err := d.DialContext(ctx, "tcp4", addr)
	if err != nil {
		return 0, err
	}
	rtt := time.Since(start)
	// RST instead of FIN: no TIME_WAIT pile-up while probing in a loop.
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetLinger(0)
	}
	c.Close()
	return rtt, nil
}

func udpEcho(ctx context.Context, addr string, via Via) (time.Duration, error) {
	d := net.Dialer{Control: via.Control}
	c, err := d.DialContext(ctx, "udp4", addr)
	if err != nil {
		return 0, err
	}
	defer c.Close()
	p := make([]byte, 48)
	copy(p, "prosvet-ping")
	binary.BigEndian.PutUint64(p[16:], uint64(time.Now().UnixNano()))
	dl := time.Now().Add(1200 * time.Millisecond)
	if d, ok := ctx.Deadline(); ok && d.Before(dl) {
		dl = d
	}
	_ = c.SetDeadline(dl)
	start := time.Now()
	if _, err := c.Write(p); err != nil {
		return 0, err
	}
	buf := make([]byte, 1500)
	if _, err := c.Read(buf); err != nil {
		return 0, err
	}
	return time.Since(start), nil
}

func shortErr(err error) string {
	var ne net.Error
	switch {
	case errors.As(err, &ne) && ne.Timeout():
		return "таймаут"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "отказ в соединении"
	case errors.Is(err, syscall.ECONNRESET):
		return "сброс"
	}
	if s := err.Error(); len(s) > 60 {
		return s[len(s)-60:]
	}
	return err.Error()
}

// DirectVia returns the interface to probe through when a VPN is the
// default route, so measurements show the path games can take around it.
func DirectVia(r netroute.Info) Via { return r.Direct() }
