package systeminfo

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

type Info struct {
	Storage        *StorageTrend `json:"storage,omitempty"`
	StorageReason  string        `json:"storage_reason,omitempty"`
	Hostname       string        "json:\"hostname\""
	OS             string        "json:\"os\""
	Arch           string        "json:\"arch\""
	UptimeSeconds  float64       "json:\"uptime_seconds\""
	LoadAverage    string        "json:\"load_average\""
	MemoryTotalKB  uint64        "json:\"memory_total_kb\""
	MemoryAvailKB  uint64        "json:\"memory_available_kb\""
	DiskTotalBytes uint64        "json:\"disk_total_bytes\""
	DiskFreeBytes  uint64        "json:\"disk_free_bytes\""
	CPUTempC       float64       "json:\"cpu_temp_c,omitempty\""
}

func Read() (Info, error) {
	var out Info
	out.Hostname, _ = os.Hostname()
	out.OS, out.Arch = runtime.GOOS, runtime.GOARCH
	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		f := strings.Fields(string(b))
		if len(f) > 0 {
			out.UptimeSeconds, _ = strconv.ParseFloat(f[0], 64)
		}
	}
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		f := strings.Fields(string(b))
		if len(f) >= 3 {
			out.LoadAverage = strings.Join(f[:3], " ")
		}
	}
	if f, err := os.Open("/proc/meminfo"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) < 2 {
				continue
			}
			v, _ := strconv.ParseUint(fields[1], 10, 64)
			switch strings.TrimSuffix(fields[0], ":") {
			case "MemTotal":
				out.MemoryTotalKB = v
			case "MemAvailable":
				out.MemoryAvailKB = v
			}
		}
		_ = f.Close()
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs("/", &st); err == nil {
		out.DiskTotalBytes = st.Blocks * uint64(st.Bsize)
		out.DiskFreeBytes = st.Bavail * uint64(st.Bsize)
	}
	for _, p := range []string{"/sys/class/thermal/thermal_zone0/temp", "/sys/devices/virtual/thermal/thermal_zone0/temp"} {
		if b, err := os.ReadFile(p); err == nil {
			v, _ := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
			if v > 1000 {
				v /= 1000
			}
			out.CPUTempC = v
			break
		}
	}
	if storage, e := ReadStorageTrend(); e == nil {
		out.Storage = &storage
	} else {
		out.StorageReason = e.Error()
	}
	return out, nil
}
