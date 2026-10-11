package core

import (
	"runtime"
	"time"

	"prosvet/internal/firewall"
	"prosvet/internal/netroute"
	"prosvet/internal/strategy"
)

// directLocked decides whether game UDP should go around a VPN tunnel:
// only on Linux with nftables, only while a game group is active, and only
// when the default route is a tunnel with a physical gateway next to it.
func (c *Controller) directLocked() *firewall.Direct {
	c.route = netroute.Detect()
	if runtime.GOOS != "linux" || c.cfg.GameDirect == "off" || c.fw.Name() != "nftables" || !c.route.CanBypass() {
		return nil
	}
	for _, g := range strategy.ActiveGroups(c.cfg.Services) {
		if g.ID == "game-udp" {
			return &firewall.Direct{Iface: c.route.Phys, Gateway: c.route.Gateway, Set: "game", Proto: g.Proto, Ports: g.Ports}
		}
	}
	return nil
}

func directEqual(a, b *firewall.Direct) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// netWatch follows VPN clients being switched on and off (or Wi-Fi
// changing gateways) and re-applies the routing within seconds.
func (c *Controller) netWatch() {
	last := netroute.Detect()
	for {
		time.Sleep(5 * time.Second)
		cur := netroute.Detect()
		if cur == last {
			continue
		}
		last = cur
		c.mu.Lock()
		enabled := c.cfg.Enabled && !(c.tune != nil && c.tune.Active)
		c.mu.Unlock()
		if !enabled {
			continue
		}
		if cur.VPN {
			c.Log.Infof("core", "сеть изменилась: маршрут по умолчанию через %s (%s)", cur.Iface, cur.App)
		} else {
			c.Log.Infof("core", "сеть изменилась: маршрут по умолчанию через %s", cur.Iface)
		}
		_ = c.Start()
	}
}

// keepBredLocked stores synthesized recipes that won something. Recipes no
// pick refers to any more are dropped beyond the newest 48.
func (c *Controller) keepBredLocked(add map[string]strategy.Bred) {
	if len(add) == 0 {
		return
	}
	if c.cfg.Bred == nil {
		c.cfg.Bred = map[string]strategy.Bred{}
	}
	for id, b := range add {
		c.cfg.Bred[id] = b
	}
	if len(c.cfg.Bred) <= 48 {
		return
	}
	used := map[string]bool{}
	for _, p := range c.cfg.Picks {
		used[p] = true
	}
	for id := range c.cfg.Bred {
		if len(c.cfg.Bred) <= 48 {
			break
		}
		if !used[id] && add[id].Args == "" {
			delete(c.cfg.Bred, id)
		}
	}
}
