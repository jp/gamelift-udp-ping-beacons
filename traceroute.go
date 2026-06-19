package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"os"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

const (
	traceMaxHops  = 20
	traceBasePort = 33434
)

type traceHop struct {
	TTL      int
	IP       net.IP
	Host     string
	LastRTT  time.Duration
	AvgRTT   time.Duration
	Sent     int
	Received int
	Error    string
}

func runCustomTrace(ctx context.Context, host string, interval, timeout time.Duration, update func(string)) error {
	if interval <= 0 {
		interval = defaultInterval
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	update(fmt.Sprintf("Resolving %s...", host))
	dstIP, err := resolveIPv4(host)
	if err != nil {
		update("resolve failed: " + err.Error())
		return err
	}

	update(fmt.Sprintf("Tracing route to %s (%s), max %d hops", host, dstIP, traceMaxHops))
	hops, err := discoverRoute(ctx, dstIP, timeout, update)
	if err != nil {
		update(err.Error())
		return err
	}
	if len(hops) == 0 {
		err := errors.New("no hops discovered")
		update(err.Error())
		return err
	}

	update(formatTraceHops(host, dstIP, hops, true))
	return pingTraceHops(ctx, host, dstIP, hops, interval, timeout, update)
}

func resolveIPv4(host string) (net.IP, error) {
	addrs, err := net.DefaultResolver.LookupIP(context.Background(), "ip4", host)
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("no IPv4 address found for %s", host)
	}
	return addrs[0], nil
}

func discoverRoute(ctx context.Context, dstIP net.IP, timeout time.Duration, update func(string)) ([]traceHop, error) {
	icmpConn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return nil, fmt.Errorf("cannot open ICMP listener: %w", err)
	}
	defer icmpConn.Close()

	udpConn, err := net.ListenPacket("udp4", "0.0.0.0:0")
	if err != nil {
		return nil, fmt.Errorf("cannot open UDP probe socket: %w", err)
	}
	defer udpConn.Close()

	ipv4Conn := ipv4.NewPacketConn(udpConn)
	dst := &net.UDPAddr{IP: dstIP, Port: traceBasePort}
	hops := make([]traceHop, 0, traceMaxHops)
	seen := map[string]bool{}

	for ttl := 1; ttl <= traceMaxHops; ttl++ {
		if ctx.Err() != nil {
			return hops, ctx.Err()
		}
		if err := ipv4Conn.SetTTL(ttl); err != nil {
			return hops, fmt.Errorf("cannot set TTL %d: %w", ttl, err)
		}

		started := time.Now()
		payload := []byte(fmt.Sprintf("gltrace:%d:%d", ttl, rand.Int()))
		if _, err := udpConn.WriteTo(payload, dst); err != nil {
			return hops, fmt.Errorf("cannot send TTL %d probe: %w", ttl, err)
		}

		hopIP, reached, err := readTraceReply(ctx, icmpConn, timeout, dstIP)
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) || isTimeout(err) {
				continue
			}
			return hops, err
		}

		key := hopIP.String()
		if !seen[key] {
			seen[key] = true
			hop := traceHop{
				TTL:     ttl,
				IP:      hopIP,
				Host:    reverseLookup(hopIP),
				LastRTT: time.Since(started),
			}
			hops = append(hops, hop)
			update(formatTraceHops("", dstIP, hops, false))
		}

		if reached {
			break
		}
	}

	return hops, nil
}

func readTraceReply(ctx context.Context, conn *icmp.PacketConn, timeout time.Duration, dstIP net.IP) (net.IP, bool, error) {
	deadline := time.Now().Add(timeout)
	buf := make([]byte, 1500)
	for {
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		if err := conn.SetReadDeadline(deadline); err != nil {
			return nil, false, err
		}
		n, peer, err := conn.ReadFrom(buf)
		if err != nil {
			return nil, false, err
		}
		msg, err := icmp.ParseMessage(1, buf[:n])
		if err != nil {
			continue
		}

		ip := addrIP(peer)
		if ip == nil {
			continue
		}

		switch msg.Type {
		case ipv4.ICMPTypeTimeExceeded:
			return ip, false, nil
		case ipv4.ICMPTypeDestinationUnreachable:
			return ip, ip.Equal(dstIP), nil
		}
	}
}

func pingTraceHops(ctx context.Context, host string, dstIP net.IP, hops []traceHop, interval, timeout time.Duration, update func(string)) error {
	conn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return fmt.Errorf("cannot open ICMP ping socket: %w", err)
	}
	defer conn.Close()

	id := os.Getpid() & 0xffff
	seq := 0
	for {
		for i := range hops {
			if ctx.Err() != nil {
				return nil
			}
			seq++
			hops[i].Sent++
			rtt, err := pingIP(ctx, conn, hops[i].IP, id, seq, timeout)
			if err != nil {
				hops[i].Error = err.Error()
			} else {
				hops[i].Received++
				hops[i].LastRTT = rtt
				hops[i].AvgRTT = rollingAvg(hops[i].AvgRTT, rtt, hops[i].Received)
				hops[i].Error = ""
			}
		}
		update(formatTraceHops(host, dstIP, hops, true))
		if !sleepContext(ctx, interval) {
			return nil
		}
	}
}

func pingIP(ctx context.Context, conn *icmp.PacketConn, ip net.IP, id, seq int, timeout time.Duration) (time.Duration, error) {
	body := &icmp.Echo{ID: id, Seq: seq, Data: []byte("gltrace-ping")}
	msg := icmp.Message{Type: ipv4.ICMPTypeEcho, Code: 0, Body: body}
	bytes, err := msg.Marshal(nil)
	if err != nil {
		return 0, err
	}

	started := time.Now()
	if _, err := conn.WriteTo(bytes, &net.IPAddr{IP: ip}); err != nil {
		return 0, err
	}

	deadline := time.Now().Add(timeout)
	buf := make([]byte, 1500)
	for {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		if err := conn.SetReadDeadline(deadline); err != nil {
			return 0, err
		}
		n, peer, err := conn.ReadFrom(buf)
		if err != nil {
			return 0, err
		}
		if !addrIP(peer).Equal(ip) {
			continue
		}
		reply, err := icmp.ParseMessage(1, buf[:n])
		if err != nil {
			continue
		}
		if reply.Type != ipv4.ICMPTypeEchoReply {
			continue
		}
		echo, ok := reply.Body.(*icmp.Echo)
		if !ok || echo.ID != id || echo.Seq != seq {
			continue
		}
		return time.Since(started), nil
	}
}

func formatTraceHops(host string, dstIP net.IP, hops []traceHop, includeStats bool) string {
	var b strings.Builder
	if host != "" {
		fmt.Fprintf(&b, "Custom traceroute to %s (%s)\n", host, dstIP)
		fmt.Fprintf(&b, "Reverse DNS is resolved for each hop. Hop pings refresh on the endpoint scan interval.\n\n")
	}
	fmt.Fprintf(&b, "%-4s %-15s %-34s", "Hop", "IP", "Reverse DNS")
	if includeStats {
		fmt.Fprintf(&b, " %5s %5s %7s %8s %8s %s", "Sent", "Recv", "Loss", "Last", "Avg", "Status")
	}
	b.WriteString("\n")
	b.WriteString(strings.Repeat("-", 96))
	b.WriteString("\n")

	sorted := append([]traceHop(nil), hops...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].TTL < sorted[j].TTL })
	for _, hop := range sorted {
		fmt.Fprintf(&b, "%-4d %-15s %-34s", hop.TTL, hop.IP, truncate(hop.Host, 34))
		if includeStats {
			loss := 0.0
			if hop.Sent > 0 {
				loss = float64(hop.Sent-hop.Received) / float64(hop.Sent) * 100
			}
			status := "ok"
			if hop.Error != "" {
				status = hop.Error
			} else if hop.Sent == 0 {
				status = "waiting"
			} else if hop.Received == 0 {
				status = "no replies"
			}
			fmt.Fprintf(&b, " %5d %5d %6.1f%% %8s %8s %s", hop.Sent, hop.Received, loss, formatDuration(hop.LastRTT), formatDuration(hop.AvgRTT), status)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func reverseLookup(ip net.IP) string {
	names, err := net.LookupAddr(ip.String())
	if err != nil || len(names) == 0 {
		return "-"
	}
	return strings.TrimSuffix(names[0], ".")
}

func rollingAvg(current, next time.Duration, count int) time.Duration {
	if count <= 1 {
		return next
	}
	return time.Duration((int64(current)*int64(count-1) + int64(next)) / int64(count))
}

func addrIP(addr net.Addr) net.IP {
	switch a := addr.(type) {
	case *net.IPAddr:
		return a.IP
	case *net.UDPAddr:
		return a.IP
	default:
		ip := net.ParseIP(strings.Split(addr.String(), ":")[0])
		return ip
	}
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func isPermissionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrPermission) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return errors.Is(opErr.Err, os.ErrPermission)
	}
	return strings.Contains(strings.ToLower(err.Error()), "operation not permitted") ||
		strings.Contains(strings.ToLower(err.Error()), "permission denied")
}

func sleepContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
