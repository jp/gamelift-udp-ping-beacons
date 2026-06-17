package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"net"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultDuration = 0
	defaultInterval = 500 * time.Millisecond
	defaultTimeout  = 1 * time.Second
	payloadMagic    = "GLPING01"
)

type endpoint struct {
	Continent   string `json:"continent"`
	Name        string `json:"name"`
	Code        string `json:"code"`
	Address     string `json:"address"`
	Protocol    string `json:"protocol"`
	IPv6Support bool   `json:"ipv6Support"`
}

type sample struct {
	Seq int           `json:"seq"`
	RTT time.Duration `json:"rtt"`
}

type result struct {
	Endpoint endpoint        `json:"endpoint"`
	Network  string          `json:"network"`
	Sent     int             `json:"sent"`
	Received int             `json:"received"`
	Lost     int             `json:"lost"`
	Loss     float64         `json:"lossPercent"`
	Min      time.Duration   `json:"minLatency"`
	Avg      time.Duration   `json:"avgLatency"`
	Max      time.Duration   `json:"maxLatency"`
	P50      time.Duration   `json:"p50Latency"`
	P95      time.Duration   `json:"p95Latency"`
	Jitter   time.Duration   `json:"jitter"`
	Samples  []time.Duration `json:"samples,omitempty"`
	Error    string          `json:"error,omitempty"`
}

type report struct {
	GeneratedAt time.Time `json:"generatedAt"`
	Duration    string    `json:"duration"`
	Interval    string    `json:"interval"`
	Timeout     string    `json:"timeout"`
	Family      string    `json:"family"`
	OS          string    `json:"os"`
	Arch        string    `json:"arch"`
	Results     []result  `json:"results"`
}

type progressEvent struct {
	Index  int
	Result result
	Done   bool
}

type pauseState struct {
	paused atomic.Bool
}

var endpoints = []endpoint{
	{"North America", "US East (N. Virginia)", "us-east-1", "gamelift-ping.us-east-1.api.aws:7770", "UDP", true},
	{"North America", "US East (Ohio)", "us-east-2", "gamelift-ping.us-east-2.api.aws:7770", "UDP", true},
	{"North America", "US West (N. California)", "us-west-1", "gamelift-ping.us-west-1.api.aws:7770", "UDP", true},
	{"North America", "US West (Oregon)", "us-west-2", "gamelift-ping.us-west-2.api.aws:7770", "UDP", true},
	{"North America", "Canada (Central)", "ca-central-1", "gamelift-ping.ca-central-1.api.aws:7770", "UDP", true},
	{"US Local Zones", "US West (Los Angeles)", "us-west-2-lax-1", "gamelift-ping-lax.us-west-2.api.aws:7770", "UDP", true},
	{"US Local Zones", "US East (Chicago)", "us-east-1-chi-1", "gamelift-ping-chi.us-east-1.api.aws:7770", "UDP", false},
	{"US Local Zones", "US East (Houston)", "us-east-1-iah-1", "gamelift-ping-iah.us-east-1.api.aws:7770", "UDP", false},
	{"US Local Zones", "US East (Dallas)", "us-east-1-dfw-1", "gamelift-ping-dfw.us-east-1.api.aws:7770", "UDP", false},
	{"US Local Zones", "US West (Denver)", "us-west-2-den-1", "gamelift-ping-den.us-west-2.api.aws:7770", "UDP", false},
	{"US Local Zones", "US East (Atlanta)", "us-east-1-atl-1", "gamelift-ping-atl.us-east-1.api.aws:7770", "UDP", false},
	{"US Local Zones", "US West (Phoenix)", "us-west-2-phx-1", "gamelift-ping-phx.us-west-2.api.aws:7770", "UDP", false},
	{"US Local Zones", "US East (Kansas City)", "us-east-1-mci-1", "gamelift-ping-mci.us-east-1.api.aws:7770", "UDP", false},
	{"South America", "South America (Sao Paulo)", "sa-east-1", "gamelift-ping.sa-east-1.api.aws:7770", "UDP", true},
	{"Europe", "Europe (Ireland)", "eu-west-1", "gamelift-ping.eu-west-1.api.aws:7770", "UDP", true},
	{"Europe", "Europe (London)", "eu-west-2", "gamelift-ping.eu-west-2.api.aws:7770", "UDP", true},
	{"Europe", "Europe (Paris)", "eu-west-3", "gamelift-ping.eu-west-3.api.aws:7770", "UDP", true},
	{"Europe", "Europe (Frankfurt)", "eu-central-1", "gamelift-ping.eu-central-1.api.aws:7770", "UDP", true},
	{"Europe", "Europe (Milan)", "eu-south-1", "gamelift-ping.eu-south-1.api.aws:7770", "UDP", true},
	{"Europe", "Europe (Stockholm)", "eu-north-1", "gamelift-ping.eu-north-1.api.aws:7770", "UDP", true},
	{"Asia Pacific", "Asia Pacific (Malaysia)", "ap-southeast-5", "gamelift-ping.ap-southeast-5.api.aws:7770", "UDP", true},
	{"Asia Pacific", "Asia Pacific (Mumbai)", "ap-south-1", "gamelift-ping.ap-south-1.api.aws:7770", "UDP", true},
	{"Asia Pacific", "Asia Pacific (Hong Kong)", "ap-east-1", "gamelift-ping.ap-east-1.api.aws:7770", "UDP", true},
	{"Asia Pacific", "Asia Pacific (Thailand)", "ap-southeast-7", "gamelift-ping.ap-southeast-7.api.aws:7770", "UDP", true},
	{"Asia Pacific", "Asia Pacific (Osaka)", "ap-northeast-3", "gamelift-ping.ap-northeast-3.api.aws:7770", "UDP", true},
	{"Asia Pacific", "Asia Pacific (Seoul)", "ap-northeast-2", "gamelift-ping.ap-northeast-2.api.aws:7770", "UDP", true},
	{"Asia Pacific", "Asia Pacific (Singapore)", "ap-southeast-1", "gamelift-ping.ap-southeast-1.api.aws:7770", "UDP", true},
	{"Asia Pacific", "Asia Pacific (Sydney)", "ap-southeast-2", "gamelift-ping.ap-southeast-2.api.aws:7770", "UDP", true},
	{"Asia Pacific", "Asia Pacific (Tokyo)", "ap-northeast-1", "gamelift-ping.ap-northeast-1.api.aws:7770", "UDP", true},
	{"Middle East", "Middle East (Bahrain)", "me-south-1", "gamelift-ping.me-south-1.api.aws:7770", "UDP", true},
	{"Africa", "Africa (Cape Town)", "af-south-1", "gamelift-ping.af-south-1.api.aws:7770", "UDP", true},
	{"Africa", "Africa (Lagos)", "af-south-1-los-1", "gamelift-ping-los.af-south-1.api.aws:7770", "UDP", false},
}

func main() {
	launchedWithoutArgs := len(os.Args) == 1
	duration := flag.Duration("duration", defaultDuration, "how long to ping each endpoint; 0 runs until quit")
	interval := flag.Duration("interval", defaultInterval, "delay between probes sent to each endpoint")
	timeout := flag.Duration("timeout", defaultTimeout, "per-probe response timeout")
	family := flag.String("family", "auto", "IP family to use: auto, ipv4, or ipv6")
	format := flag.String("format", "table", "report format: table or json")
	pause := flag.String("pause", "auto", "pause before exiting: auto, always, or never")
	includeSamples := flag.Bool("samples", false, "include raw RTT samples in JSON output")
	flag.Parse()

	if err := validateFlags(*duration, *interval, *timeout, *family, *format, *pause); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		maybePause(*pause, launchedWithoutArgs)
		os.Exit(2)
	}

	if *format == "table" && stdoutIsTerminal() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if err := runTUI(ctx, endpoints, *duration, *interval, *timeout, *family); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			maybePause(*pause, launchedWithoutArgs)
			os.Exit(1)
		}
		return
	}

	effectiveDuration := *duration
	if effectiveDuration == 0 {
		effectiveDuration = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), effectiveDuration)
	defer cancel()

	var progress chan progressEvent
	var progressDone chan struct{}
	showProgress := *format == "table"
	if showProgress {
		printScanIntro(len(endpoints), effectiveDuration, *interval, *timeout, *family)
		fmt.Println("Scanning in progress...")
	}

	results := runAll(ctx, endpoints, effectiveDuration, *interval, *timeout, *family, progress, nil)
	if progress != nil {
		close(progress)
		<-progressDone
	} else if showProgress {
		fmt.Println("Scan complete.")
		fmt.Println()
	}
	if !*includeSamples {
		for i := range results {
			results[i].Samples = nil
		}
	}

	rep := report{
		GeneratedAt: time.Now().UTC(),
		Duration:    effectiveDuration.String(),
		Interval:    interval.String(),
		Timeout:     timeout.String(),
		Family:      *family,
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		Results:     results,
	}

	switch *format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			maybePause(*pause, launchedWithoutArgs)
			os.Exit(1)
		}
	default:
		printTable(rep)
	}

	maybePause(*pause, launchedWithoutArgs)
}

func validateFlags(duration, interval, timeout time.Duration, family, format, pause string) error {
	if duration < 0 {
		return errors.New("duration must be zero or greater")
	}
	if interval <= 0 {
		return errors.New("interval must be greater than zero")
	}
	if timeout <= 0 {
		return errors.New("timeout must be greater than zero")
	}
	switch family {
	case "auto", "ipv4", "ipv6":
	default:
		return errors.New("family must be auto, ipv4, or ipv6")
	}
	switch format {
	case "table", "json":
	default:
		return errors.New("format must be table or json")
	}
	switch pause {
	case "auto", "always", "never":
	default:
		return errors.New("pause must be auto, always, or never")
	}
	return nil
}

func maybePause(pause string, launchedWithoutArgs bool) {
	if !shouldPause(pause, runtime.GOOS, launchedWithoutArgs, stdinIsTerminal()) {
		return
	}

	fmt.Fprint(os.Stdout, "\nPress Enter to quit...")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}

func shouldPause(pause, goos string, launchedWithoutArgs, stdinIsTerminal bool) bool {
	if !stdinIsTerminal {
		return false
	}

	switch pause {
	case "always":
		return true
	case "auto":
		return goos == "windows" && launchedWithoutArgs
	default:
		return false
	}
}

func stdinIsTerminal() bool {
	return fileIsTerminal(os.Stdin)
}

func stdoutIsTerminal() bool {
	return fileIsTerminal(os.Stdout)
}

func fileIsTerminal(file *os.File) bool {
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func runAll(ctx context.Context, endpoints []endpoint, duration, interval, timeout time.Duration, family string, progress chan<- progressEvent, pause *pauseState) []result {
	results := make([]result, len(endpoints))
	var wg sync.WaitGroup

	for i, ep := range endpoints {
		wg.Add(1)
		go func(i int, ep endpoint) {
			defer wg.Done()
			results[i] = pingEndpoint(ctx, i, ep, duration, interval, timeout, family, progress, pause)
		}(i, ep)
	}

	wg.Wait()
	sort.SliceStable(results, func(i, j int) bool {
		if (results[i].Error != "") != (results[j].Error != "") {
			return results[i].Error == ""
		}
		if results[i].Loss != results[j].Loss {
			return results[i].Loss < results[j].Loss
		}
		if results[i].Avg != results[j].Avg {
			return results[i].Avg < results[j].Avg
		}
		return results[i].Endpoint.Code < results[j].Endpoint.Code
	})
	return results
}

func printScanIntro(endpointCount int, duration, interval, timeout time.Duration, family string) {
	fmt.Printf("Scanning %d GameLift UDP ping beacons.\n", endpointCount)
	fmt.Printf("Each region is tested for %s with one UDP probe every %s. A reply must arrive within %s.\n", duration, interval, timeout)
	fmt.Printf("The report will show latency, jitter, and packet loss for each endpoint. IP family: %s.\n\n", family)
}

func showScanProgress(done chan<- struct{}, progress <-chan progressEvent, total int, duration time.Duration) {
	defer close(done)

	frames := []string{"|", "/", "-", "\\"}
	started := time.Now()
	completed := 0
	ticker := time.NewTicker(120 * time.Millisecond)
	defer ticker.Stop()

	render := func(frame string) {
		elapsed := time.Since(started).Truncate(time.Second)
		remaining := duration - time.Since(started)
		if remaining < 0 {
			remaining = 0
		}
		fmt.Printf("\r%s scanning endpoints... %d/%d complete, elapsed %s, about %s remaining   ",
			frame, completed, total, elapsed, remaining.Truncate(time.Second))
	}

	render(frames[0])
	frameIndex := 0
	for {
		select {
		case event, ok := <-progress:
			if !ok {
				completed = total
				render("done")
				fmt.Print("\n\n")
				return
			}
			if event.Done {
				completed++
			}
			if completed > total {
				completed = total
			}
		case <-ticker.C:
			frameIndex = (frameIndex + 1) % len(frames)
		}
		render(frames[frameIndex])
	}
}

func pingEndpoint(ctx context.Context, index int, ep endpoint, duration, interval, timeout time.Duration, family string, progress chan<- progressEvent, pause *pauseState) result {
	res := result{Endpoint: ep, Network: networkFor(ep, family)}
	defer func() {
		sendProgress(ctx, progress, progressEvent{Index: index, Result: snapshotResult(res), Done: true})
	}()

	if res.Network == "" {
		res.Error = "endpoint is not marked as IPv6-capable"
		return res
	}

	addr, err := net.ResolveUDPAddr(res.Network, ep.Address)
	if err != nil {
		res.Error = err.Error()
		return res
	}

	conn, err := net.DialUDP(res.Network, nil, addr)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer conn.Close()

	var deadline time.Time
	if duration > 0 {
		deadline = time.Now().Add(duration)
	}
	nextSend := time.Now()
	buf := make([]byte, 128)

	for seq := 0; ; seq++ {
		if !waitIfPaused(ctx, pause) {
			break
		}

		now := time.Now()
		if deadlineReached(now, deadline) || ctx.Err() != nil {
			break
		}
		if now.Before(nextSend) {
			sleepUntil(ctx, nextSend)
			if ctx.Err() != nil || deadlineReached(time.Now(), deadline) {
				break
			}
		}

		res.Sent++
		payload := makePayload(seq)
		sentAt := time.Now()
		if _, err := conn.Write(payload); err != nil {
			res.Error = err.Error()
			sendProgress(ctx, progress, progressEvent{Index: index, Result: snapshotResult(res)})
			break
		}

		readUntil := sentAt.Add(timeout)
		if !deadline.IsZero() {
			readUntil = minTime(readUntil, deadline)
		}
		if err := conn.SetReadDeadline(readUntil); err != nil {
			res.Error = err.Error()
			sendProgress(ctx, progress, progressEvent{Index: index, Result: snapshotResult(res)})
			break
		}

		n, err := conn.Read(buf)
		if err == nil && n > 0 {
			res.Received++
			res.Samples = append(res.Samples, time.Since(sentAt))
		} else if err != nil {
			var netErr net.Error
			if !errors.As(err, &netErr) || !netErr.Timeout() {
				res.Error = err.Error()
				sendProgress(ctx, progress, progressEvent{Index: index, Result: snapshotResult(res)})
				break
			}
		}

		sendProgress(ctx, progress, progressEvent{Index: index, Result: snapshotResult(res)})
		nextSend = sentAt.Add(interval)
	}

	finalizeResult(&res)
	return res
}

func (p *pauseState) toggle() bool {
	if p == nil {
		return false
	}
	next := !p.paused.Load()
	p.paused.Store(next)
	return next
}

func (p *pauseState) isPaused() bool {
	return p != nil && p.paused.Load()
}

func waitIfPaused(ctx context.Context, pause *pauseState) bool {
	for pause != nil && pause.isPaused() {
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
	return ctx.Err() == nil
}

func deadlineReached(now, deadline time.Time) bool {
	return !deadline.IsZero() && now.After(deadline)
}

func snapshotResult(res result) result {
	res.Samples = append([]time.Duration(nil), res.Samples...)
	finalizeResult(&res)
	return res
}

func sendProgress(ctx context.Context, progress chan<- progressEvent, event progressEvent) {
	if progress == nil {
		return
	}
	select {
	case progress <- event:
	case <-ctx.Done():
	}
}

func networkFor(ep endpoint, family string) string {
	switch family {
	case "ipv4":
		return "udp4"
	case "ipv6":
		if !ep.IPv6Support {
			return ""
		}
		return "udp6"
	default:
		if ep.IPv6Support {
			return "udp"
		}
		return "udp4"
	}
}

func makePayload(seq int) []byte {
	payload := make([]byte, len(payloadMagic)+16)
	copy(payload, payloadMagic)
	binary.BigEndian.PutUint64(payload[len(payloadMagic):], uint64(seq))
	binary.BigEndian.PutUint64(payload[len(payloadMagic)+8:], uint64(time.Now().UnixNano()))
	return payload
}

func sleepUntil(ctx context.Context, t time.Time) {
	timer := time.NewTimer(time.Until(t))
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func finalizeResult(res *result) {
	res.Lost = res.Sent - res.Received
	if res.Sent > 0 {
		res.Loss = float64(res.Lost) / float64(res.Sent) * 100
	}
	if len(res.Samples) == 0 {
		return
	}

	samples := append([]time.Duration(nil), res.Samples...)
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })

	var total time.Duration
	res.Min = samples[0]
	res.Max = samples[len(samples)-1]
	for _, rtt := range samples {
		total += rtt
	}
	res.Avg = total / time.Duration(len(samples))
	res.P50 = percentile(samples, 50)
	res.P95 = percentile(samples, 95)
	res.Jitter = meanAbsDelta(res.Samples)
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	rank := (p / 100) * float64(len(sorted)-1)
	lo := int(math.Floor(rank))
	hi := int(math.Ceil(rank))
	if lo == hi {
		return sorted[lo]
	}
	weight := rank - float64(lo)
	return time.Duration(float64(sorted[lo])*(1-weight) + float64(sorted[hi])*weight)
}

func meanAbsDelta(samples []time.Duration) time.Duration {
	if len(samples) < 2 {
		return 0
	}
	var total time.Duration
	for i := 1; i < len(samples); i++ {
		delta := samples[i] - samples[i-1]
		if delta < 0 {
			delta = -delta
		}
		total += delta
	}
	return total / time.Duration(len(samples)-1)
}

func printTable(rep report) {
	fmt.Printf("GameLift UDP ping beacon report\n")
	fmt.Printf("Generated: %s\n", rep.GeneratedAt.Format(time.RFC3339))
	fmt.Printf("Host: %s/%s  Duration: %s  Interval: %s  Timeout: %s  Family: %s\n\n",
		rep.OS, rep.Arch, rep.Duration, rep.Interval, rep.Timeout, rep.Family)

	fmt.Printf("%-16s %-26s %-44s %5s %5s %7s %8s %8s %8s %8s %8s %s\n",
		"Code", "Location", "Address", "Sent", "Recv", "Loss", "Avg", "P50", "P95", "Jitter", "Min", "Status")
	fmt.Println(strings.Repeat("-", 168))

	for _, res := range rep.Results {
		status := "ok"
		if res.Error != "" {
			status = res.Error
		} else if res.Received == 0 {
			status = "no replies"
		}
		fmt.Printf("%-16s %-26s %-44s %5d %5d %6.1f%% %8s %8s %8s %8s %8s %s\n",
			res.Endpoint.Code,
			truncate(res.Endpoint.Name, 26),
			truncate(hostOnly(res.Endpoint.Address), 44),
			res.Sent,
			res.Received,
			res.Loss,
			formatDuration(res.Avg),
			formatDuration(res.P50),
			formatDuration(res.P95),
			formatDuration(res.Jitter),
			formatDuration(res.Min),
			status)
	}
}

func formatDuration(d time.Duration) string {
	if d == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1fms", float64(d.Microseconds())/1000)
}

func truncate(s string, width int) string {
	if len(s) <= width {
		return s
	}
	if width <= 1 {
		return s[:width]
	}
	return s[:width-1] + "."
}

func hostOnly(address string) string {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return address
	}
	return host
}
