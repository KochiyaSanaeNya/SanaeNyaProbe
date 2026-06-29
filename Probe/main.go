//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	defaultConfigPath = "/etc/SanaeNyaProbe/probe.json"
	probeInterval     = 3 * time.Second
	requestTimeout    = 2 * time.Second
	sectorSize        = 512
)

type config struct {
	ServerURL string `json:"server_url"`
	Name      string `json:"name"`
	UUID      string `json:"uuid"`
}

type memInfo struct {
	Total     uint64
	Available uint64
	Used      uint64
	SwapTotal uint64
	SwapFree  uint64
	SwapUsed  uint64
}

type loadInfo struct {
	Load1  float64
	Load5  float64
	Active float64
}

type storageUsage struct {
	MountPoint     string  `json:"mount_point"`
	FileSystem     string  `json:"file_system"`
	TotalBytes     uint64  `json:"total_bytes"`
	UsedBytes      uint64  `json:"used_bytes"`
	FreeBytes      uint64  `json:"free_bytes"`
	AvailableBytes uint64  `json:"available_bytes"`
	UsedPercent    float64 `json:"used_percent"`
}

type diskCounters struct {
	ReadBytes   uint64
	WriteBytes  uint64
	ReadOps     uint64
	WriteOps    uint64
	DeviceCount int
}

type netCounters struct {
	RxBytes        uint64
	TxBytes        uint64
	InterfaceCount int
}

type rates struct {
	DiskReady     bool
	NetReady      bool
	DiskReadBPS   float64
	DiskWriteBPS  float64
	DiskReadIOPS  float64
	DiskWriteIOPS float64
	NetRxBPS      float64
	NetTxBPS      float64
}

type rateSampler struct {
	diskReady bool
	diskAt    time.Time
	disk      diskCounters
	netReady  bool
	netAt     time.Time
	net       netCounters
}

type load3Sampler struct {
	ready bool
	at    time.Time
	value float64
}

type collector struct {
	rates rateSampler
	load3 load3Sampler
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	configPath := flag.String("config", defaultConfigPath, "JSON config file path")
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := &http.Client{
		Timeout: requestTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 0 && via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
				return http.ErrUseLastResponse
			}
			if len(via) >= 3 {
				return http.ErrUseLastResponse
			}
			return nil
		},
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			MaxIdleConns:          2,
			MaxIdleConnsPerHost:   1,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   requestTimeout,
			ResponseHeaderTimeout: requestTimeout,
			ExpectContinueTimeout: time.Second,
		},
	}

	probe := &collector{}
	log.Printf("SanaeNyaProbe started, receiver=%s interval=%s", cfg.ServerURL, probeInterval)
	run(ctx, client, cfg, probe)
	log.Print("SanaeNyaProbe stopped")
}

func run(ctx context.Context, client *http.Client, cfg config, probe *collector) {
	ticker := time.NewTicker(probeInterval)
	defer ticker.Stop()

	for {
		form := probe.collect(cfg)
		if err := postForm(ctx, client, cfg.ServerURL, form); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("post metrics: %v", err)
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func loadConfig(path string) (config, error) {
	file, err := os.Open(path)
	if err != nil {
		return config{}, err
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()

	var cfg config
	if err := decoder.Decode(&cfg); err != nil {
		return config{}, err
	}
	if decoder.More() {
		return config{}, fmt.Errorf("config contains extra JSON values")
	}

	cfg.ServerURL = strings.TrimSpace(cfg.ServerURL)
	cfg.Name = strings.TrimSpace(cfg.Name)
	cfg.UUID = strings.TrimSpace(cfg.UUID)

	if cfg.ServerURL == "" {
		return config{}, fmt.Errorf("server_url is required")
	}
	if cfg.Name == "" {
		return config{}, fmt.Errorf("name is required")
	}
	if cfg.UUID == "" {
		return config{}, fmt.Errorf("uuid is required")
	}

	if !strings.Contains(cfg.ServerURL, "://") {
		cfg.ServerURL = "https://" + cfg.ServerURL
	}

	parsed, err := url.Parse(cfg.ServerURL)
	if err != nil {
		return config{}, fmt.Errorf("server_url is invalid: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return config{}, fmt.Errorf("server_url must include a host")
	}
	if parsed.Scheme != "https" {
		return config{}, fmt.Errorf("server_url scheme must be https or omitted for default HTTPS")
	}
	cfg.ServerURL = parsed.String()

	return cfg, nil
}

func (c *collector) collect(cfg config) url.Values {
	now := time.Now()
	form := url.Values{}
	var errs []string

	form.Set("schema_version", "1")
	form.Set("name", cfg.Name)
	form.Set("uuid", cfg.UUID)
	form.Set("timestamp_unix", strconv.FormatInt(now.Unix(), 10))

	if uptime, err := readUptime(); err == nil {
		form.Set("uptime_sec", formatFloat(uptime, 2))
	} else {
		errs = append(errs, "uptime: "+err.Error())
	}

	if load, err := readLoad(); err == nil {
		load3 := c.load3.update(now, load)
		form.Set("load1", formatFloat(load.Load1, 2))
		form.Set("load3", formatFloat(load3, 2))
		form.Set("load5", formatFloat(load.Load5, 2))
	} else {
		errs = append(errs, "load: "+err.Error())
	}

	if mem, err := readMemInfo(); err == nil {
		form.Set("mem_total_bytes", strconv.FormatUint(mem.Total, 10))
		form.Set("mem_used_bytes", strconv.FormatUint(mem.Used, 10))
		form.Set("mem_available_bytes", strconv.FormatUint(mem.Available, 10))
		form.Set("mem_used_percent", formatFloat(percent(mem.Used, mem.Total), 2))
		form.Set("swap_total_bytes", strconv.FormatUint(mem.SwapTotal, 10))
		form.Set("swap_used_bytes", strconv.FormatUint(mem.SwapUsed, 10))
		form.Set("swap_free_bytes", strconv.FormatUint(mem.SwapFree, 10))
		form.Set("swap_used_percent", formatFloat(percent(mem.SwapUsed, mem.SwapTotal), 2))
	} else {
		errs = append(errs, "memory: "+err.Error())
	}

	if storages, err := readStorageUsage(); err == nil {
		if len(storages) > 0 {
			root := chooseRootStorage(storages)
			form.Set("storage_total_bytes", strconv.FormatUint(root.TotalBytes, 10))
			form.Set("storage_used_bytes", strconv.FormatUint(root.UsedBytes, 10))
			form.Set("storage_free_bytes", strconv.FormatUint(root.FreeBytes, 10))
			form.Set("storage_available_bytes", strconv.FormatUint(root.AvailableBytes, 10))
			form.Set("storage_used_percent", formatFloat(root.UsedPercent, 2))
		}
		if encoded, err := json.Marshal(storages); err == nil {
			form.Set("storage", string(encoded))
		} else {
			errs = append(errs, "storage_json: "+err.Error())
		}
	} else {
		errs = append(errs, "storage: "+err.Error())
	}

	disk, diskErr := readDiskCounters()
	if diskErr != nil {
		errs = append(errs, "disk_io: "+diskErr.Error())
	}
	net, netErr := readNetCounters()
	if netErr != nil {
		errs = append(errs, "network: "+netErr.Error())
	}

	currentRates := c.rates.update(now, disk, diskErr == nil, net, netErr == nil)
	form.Set("disk_rates_ready", boolField(currentRates.DiskReady))
	form.Set("net_rates_ready", boolField(currentRates.NetReady))
	if diskErr == nil {
		form.Set("disk_read_bytes", strconv.FormatUint(disk.ReadBytes, 10))
		form.Set("disk_write_bytes", strconv.FormatUint(disk.WriteBytes, 10))
		form.Set("disk_read_bps", formatFloat(currentRates.DiskReadBPS, 2))
		form.Set("disk_write_bps", formatFloat(currentRates.DiskWriteBPS, 2))
		form.Set("disk_read_iops", formatFloat(currentRates.DiskReadIOPS, 2))
		form.Set("disk_write_iops", formatFloat(currentRates.DiskWriteIOPS, 2))
		form.Set("disk_device_count", strconv.Itoa(disk.DeviceCount))
	}
	if netErr == nil {
		form.Set("net_rx_bytes", strconv.FormatUint(net.RxBytes, 10))
		form.Set("net_tx_bytes", strconv.FormatUint(net.TxBytes, 10))
		form.Set("net_rx_bps", formatFloat(currentRates.NetRxBPS, 2))
		form.Set("net_tx_bps", formatFloat(currentRates.NetTxBPS, 2))
		form.Set("net_interface_count", strconv.Itoa(net.InterfaceCount))
	}

	if len(errs) > 0 {
		form.Set("errors", strings.Join(errs, "; "))
	}
	return form
}

func postForm(ctx context.Context, client *http.Client, endpoint string, form url.Values) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "SanaeNyaProbe/0.1")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("receiver returned %s", resp.Status)
	}
	return nil
}

func readUptime() (float64, error) {
	content, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(content))
	if len(fields) == 0 {
		return 0, fmt.Errorf("/proc/uptime is empty")
	}
	return strconv.ParseFloat(fields[0], 64)
}

func readLoad() (loadInfo, error) {
	content, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return loadInfo{}, err
	}
	fields := strings.Fields(string(content))
	if len(fields) < 4 {
		return loadInfo{}, fmt.Errorf("unexpected /proc/loadavg format")
	}
	load1, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return loadInfo{}, err
	}
	load5, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return loadInfo{}, err
	}
	active := readActiveTasks(fields[3])
	return loadInfo{Load1: load1, Load5: load5, Active: active}, nil
}

func readActiveTasks(loadavgTaskField string) float64 {
	parts := strings.Split(loadavgTaskField, "/")
	if len(parts) > 0 {
		if running, err := strconv.ParseFloat(parts[0], 64); err == nil {
			return running
		}
	}
	content, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0
	}
	var running, blocked float64
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		switch fields[0] {
		case "procs_running":
			running, _ = strconv.ParseFloat(fields[1], 64)
		case "procs_blocked":
			blocked, _ = strconv.ParseFloat(fields[1], 64)
		}
	}
	return running + blocked
}

func (s *load3Sampler) update(now time.Time, load loadInfo) float64 {
	if !s.ready {
		s.ready = true
		s.at = now
		if load.Load1 > 0 || load.Load5 > 0 {
			s.value = (load.Load1 + load.Load5) / 2
		} else {
			s.value = load.Active
		}
		return s.value
	}

	dt := now.Sub(s.at).Seconds()
	if dt <= 0 {
		return s.value
	}
	decay := math.Exp(-dt / 180.0)
	s.value = s.value*decay + load.Active*(1-decay)
	s.at = now
	return s.value
}

func readMemInfo() (memInfo, error) {
	content, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return memInfo{}, err
	}

	values := make(map[string]uint64, 8)
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		values[key] = value * 1024
	}

	total := values["MemTotal"]
	if total == 0 {
		return memInfo{}, fmt.Errorf("MemTotal missing")
	}
	available, ok := values["MemAvailable"]
	if !ok {
		available = estimatedMemAvailable(values)
	}
	if available > total {
		available = total
	}
	swapTotal := values["SwapTotal"]
	swapFree := values["SwapFree"]
	if swapFree > swapTotal {
		swapFree = swapTotal
	}

	return memInfo{
		Total:     total,
		Available: available,
		Used:      total - available,
		SwapTotal: swapTotal,
		SwapFree:  swapFree,
		SwapUsed:  swapTotal - swapFree,
	}, nil
}

func estimatedMemAvailable(values map[string]uint64) uint64 {
	available := values["MemFree"] + values["Buffers"] + values["Cached"] + values["SReclaimable"]
	if shmem := values["Shmem"]; shmem < available {
		available -= shmem
	} else {
		available = 0
	}
	return available
}

func readStorageUsage() ([]storageUsage, error) {
	mounts, err := readMounts()
	if err != nil {
		return nil, err
	}

	seen := make(map[uint64]struct{}, len(mounts))
	storages := make([]storageUsage, 0, len(mounts))
	for _, mount := range mounts {
		var stat syscall.Statfs_t
		if err := syscall.Statfs(mount.mountPoint, &stat); err != nil {
			continue
		}
		devID := uint64(stat.Fsid.X__val[0])<<32 | uint64(uint32(stat.Fsid.X__val[1]))
		if _, ok := seen[devID]; ok && mount.mountPoint != "/" {
			continue
		}
		seen[devID] = struct{}{}

		total := stat.Blocks * uint64(stat.Bsize)
		free := stat.Bfree * uint64(stat.Bsize)
		available := stat.Bavail * uint64(stat.Bsize)
		used := uint64(0)
		if total > free {
			used = total - free
		}

		storages = append(storages, storageUsage{
			MountPoint:     mount.mountPoint,
			FileSystem:     mount.fsType,
			TotalBytes:     total,
			UsedBytes:      used,
			FreeBytes:      free,
			AvailableBytes: available,
			UsedPercent:    percent(used, total),
		})
	}

	if len(storages) == 0 {
		root, err := statStorage("/", "unknown")
		if err != nil {
			return nil, err
		}
		storages = append(storages, root)
	}
	return storages, nil
}

type mountInfo struct {
	mountPoint string
	fsType     string
}

func readMounts() ([]mountInfo, error) {
	content, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}

	mounts := make([]mountInfo, 0, 8)
	for _, line := range strings.Split(string(content), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, " - ")
		if len(parts) != 2 {
			continue
		}
		before := strings.Fields(parts[0])
		after := strings.Fields(parts[1])
		if len(before) < 5 || len(after) < 1 {
			continue
		}
		mountPoint := unescapeMountPath(before[4])
		fsType := after[0]
		if shouldSkipMount(mountPoint, fsType) {
			continue
		}
		mounts = append(mounts, mountInfo{mountPoint: mountPoint, fsType: fsType})
	}
	return mounts, nil
}

func statStorage(path, fsType string) (storageUsage, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return storageUsage{}, err
	}
	total := stat.Blocks * uint64(stat.Bsize)
	free := stat.Bfree * uint64(stat.Bsize)
	available := stat.Bavail * uint64(stat.Bsize)
	used := uint64(0)
	if total > free {
		used = total - free
	}
	return storageUsage{
		MountPoint:     path,
		FileSystem:     fsType,
		TotalBytes:     total,
		UsedBytes:      used,
		FreeBytes:      free,
		AvailableBytes: available,
		UsedPercent:    percent(used, total),
	}, nil
}

func chooseRootStorage(storages []storageUsage) storageUsage {
	for _, storage := range storages {
		if storage.MountPoint == "/" {
			return storage
		}
	}
	return storages[0]
}

func shouldSkipMount(mountPoint, fsType string) bool {
	switch fsType {
	case "9p", "afs", "autofs", "binfmt_misc", "bpf", "cgroup", "cgroup2", "ceph", "cifs", "configfs", "debugfs", "devpts", "devtmpfs", "fuse", "fuseblk", "fusectl", "gfs", "gfs2", "glusterfs", "hugetlbfs", "mqueue", "ncpfs", "nfs", "nfs4", "nsfs", "ocfs2", "proc", "pstore", "securityfs", "smb3", "smbfs", "sshfs", "sysfs", "tracefs", "tmpfs":
		return true
	}
	if strings.HasPrefix(fsType, "fuse.") {
		return true
	}
	if mountPoint == "/proc" || mountPoint == "/sys" || mountPoint == "/dev" || mountPoint == "/run" {
		return true
	}
	for _, prefix := range []string{"/proc/", "/sys/", "/dev/", "/run/"} {
		if strings.HasPrefix(mountPoint, prefix) {
			return true
		}
	}
	return false
}

func unescapeMountPath(path string) string {
	var builder strings.Builder
	builder.Grow(len(path))
	for i := 0; i < len(path); i++ {
		if path[i] == '\\' && i+3 < len(path) && isOctal(path[i+1]) && isOctal(path[i+2]) && isOctal(path[i+3]) {
			value := (path[i+1]-'0')*64 + (path[i+2]-'0')*8 + (path[i+3] - '0')
			builder.WriteByte(value)
			i += 3
			continue
		}
		builder.WriteByte(path[i])
	}
	return builder.String()
}

func isOctal(b byte) bool {
	return b >= '0' && b <= '7'
}

func readDiskCounters() (diskCounters, error) {
	content, err := os.ReadFile("/proc/diskstats")
	if err != nil {
		return diskCounters{}, err
	}

	var counters diskCounters
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}
		name := fields[2]
		if !shouldCountDisk(name) {
			continue
		}
		readOps, err := strconv.ParseUint(fields[3], 10, 64)
		if err != nil {
			continue
		}
		readSectors, err := strconv.ParseUint(fields[5], 10, 64)
		if err != nil {
			continue
		}
		writeOps, err := strconv.ParseUint(fields[7], 10, 64)
		if err != nil {
			continue
		}
		writeSectors, err := strconv.ParseUint(fields[9], 10, 64)
		if err != nil {
			continue
		}
		counters.ReadOps += readOps
		counters.WriteOps += writeOps
		counters.ReadBytes += readSectors * sectorSize
		counters.WriteBytes += writeSectors * sectorSize
		counters.DeviceCount++
	}
	return counters, nil
}

func shouldCountDisk(name string) bool {
	for _, prefix := range []string{"loop", "ram", "fd", "sr", "zram", "dm-", "md"} {
		if strings.HasPrefix(name, prefix) {
			return false
		}
	}
	if _, err := os.Stat(filepath.Join("/sys/block", name)); err == nil {
		return true
	}
	return isLikelyWholeDiskName(name)
}

func isLikelyWholeDiskName(name string) bool {
	if strings.HasPrefix(name, "nvme") {
		return !strings.Contains(name, "p")
	}
	if strings.HasPrefix(name, "mmcblk") {
		return !strings.Contains(name, "p")
	}
	for i := 0; i < len(name); i++ {
		if name[i] >= '0' && name[i] <= '9' {
			return false
		}
	}
	return true
}

func readNetCounters() (netCounters, error) {
	content, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return netCounters{}, err
	}

	var counters netCounters
	for _, line := range strings.Split(string(content), "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		iface := strings.TrimSpace(parts[0])
		if iface == "" || iface == "lo" {
			continue
		}
		fields := strings.Fields(parts[1])
		if len(fields) < 16 {
			continue
		}
		rx, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		tx, err := strconv.ParseUint(fields[8], 10, 64)
		if err != nil {
			continue
		}
		counters.RxBytes += rx
		counters.TxBytes += tx
		counters.InterfaceCount++
	}
	return counters, nil
}

func (s *rateSampler) update(now time.Time, disk diskCounters, diskOK bool, net netCounters, netOK bool) rates {
	var current rates

	if diskOK {
		if s.diskReady {
			if dt := now.Sub(s.diskAt).Seconds(); dt > 0 {
				current.DiskReady = true
				current.DiskReadBPS = float64(counterDelta(disk.ReadBytes, s.disk.ReadBytes)) / dt
				current.DiskWriteBPS = float64(counterDelta(disk.WriteBytes, s.disk.WriteBytes)) / dt
				current.DiskReadIOPS = float64(counterDelta(disk.ReadOps, s.disk.ReadOps)) / dt
				current.DiskWriteIOPS = float64(counterDelta(disk.WriteOps, s.disk.WriteOps)) / dt
			}
		}
		s.diskReady = true
		s.diskAt = now
		s.disk = disk
	}

	if netOK {
		if s.netReady {
			if dt := now.Sub(s.netAt).Seconds(); dt > 0 {
				current.NetReady = true
				current.NetRxBPS = float64(counterDelta(net.RxBytes, s.net.RxBytes)) / dt
				current.NetTxBPS = float64(counterDelta(net.TxBytes, s.net.TxBytes)) / dt
			}
		}
		s.netReady = true
		s.netAt = now
		s.net = net
	}

	return current
}

func counterDelta(current, previous uint64) uint64 {
	if current < previous {
		return 0
	}
	return current - previous
}

func percent(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(used) * 100 / float64(total)
}

func boolField(ok bool) string {
	if ok {
		return "1"
	}
	return "0"
}

func formatFloat(value float64, precision int) string {
	return strconv.FormatFloat(value, 'f', precision, 64)
}
