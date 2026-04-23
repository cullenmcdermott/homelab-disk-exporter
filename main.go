package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/sys/unix"
)

// Paths — host filesystem is mounted under /host
const (
	hostSysBlock = "/host/sys/block"
	hostDevPath  = "/host/dev"
	hostRookPath = "/host/var/lib/rook/rook-ceph"
)

// NVMe ioctl constants
const (
	nvmeAdminCmd    = 0xC0484E41 // NVME_IOCTL_ADMIN_CMD
	nvmeGetLogPage  = 0x02       // Admin opcode
	smartLogID      = 0x02
	smartLogSize    = 512
)

// nvmeAdminCommand matches the kernel struct for ioctl
type nvmeAdminCommand struct {
	Opcode      uint8
	Flags       uint8
	Rsvd1       uint16
	Nsid        uint32
	Cdw2        uint32
	Cdw3        uint32
	Metadata    uint64
	Addr        uint64
	MetadataLen uint32
	DataLen     uint32
	Cdw10       uint32
	Cdw11       uint32
	Cdw12       uint32
	Cdw13       uint32
	Cdw14       uint32
	Cdw15       uint32
	TimeoutMs   uint32
	Result      uint32
}

type diskInfo struct {
	Device      string
	Model       string
	Serial      string
	Firmware    string
	Transport   string
	Rotational  string
	SizeBytes   float64
	CephOSD     int // -1 if not a Ceph device

	// SMART data
	Temperature      float64
	PercentageUsed   float64
	AvailableSpare   float64
	PowerOnHours     float64
	DataReadBytes    float64
	DataWrittenBytes float64
	UnsafeShutdowns  float64
	MediaErrors      float64
	CriticalWarning  float64
	HasSMART         bool
}

type diskCollector struct {
	nodeName string

	// Descriptors
	infoDesc          *prometheus.Desc
	sizeDesc          *prometheus.Desc
	tempDesc          *prometheus.Desc
	usedDesc          *prometheus.Desc
	spareDesc         *prometheus.Desc
	pohDesc           *prometheus.Desc
	readDesc          *prometheus.Desc
	writtenDesc       *prometheus.Desc
	unsafeDesc        *prometheus.Desc
	mediaErrDesc      *prometheus.Desc
	critWarnDesc      *prometheus.Desc
	scrapeDurDesc     *prometheus.Desc
	devicesTotalDesc  *prometheus.Desc
}

func newDiskCollector(nodeName string) *diskCollector {
	labels := []string{"node", "device"}
	infoLabels := []string{"node", "device", "model", "serial", "firmware", "transport", "rotational", "ceph_osd"}

	c := &diskCollector{
		nodeName:         nodeName,
		infoDesc:         prometheus.NewDesc("homelab_disk_info", "Disk info metric", infoLabels, nil),
		sizeDesc:         prometheus.NewDesc("homelab_disk_size_bytes", "Disk size in bytes", labels, nil),
		tempDesc:         prometheus.NewDesc("homelab_disk_temperature_celsius", "Disk temperature", labels, nil),
		usedDesc:         prometheus.NewDesc("homelab_disk_percentage_used", "NVMe percentage used indicator", labels, nil),
		spareDesc:        prometheus.NewDesc("homelab_disk_available_spare_percent", "NVMe available spare", labels, nil),
		pohDesc:          prometheus.NewDesc("homelab_disk_power_on_hours_total", "Power-on hours", labels, nil),
		readDesc:         prometheus.NewDesc("homelab_disk_data_read_bytes_total", "Total data read in bytes", labels, nil),
		writtenDesc:      prometheus.NewDesc("homelab_disk_data_written_bytes_total", "Total data written in bytes", labels, nil),
		unsafeDesc:       prometheus.NewDesc("homelab_disk_unsafe_shutdowns_total", "Unsafe shutdowns count", labels, nil),
		mediaErrDesc:     prometheus.NewDesc("homelab_disk_media_errors_total", "Media errors count", labels, nil),
		critWarnDesc:     prometheus.NewDesc("homelab_disk_critical_warning", "NVMe critical warning bitmask", labels, nil),
		scrapeDurDesc:    prometheus.NewDesc("homelab_disk_scrape_duration_seconds", "Time to collect disk metrics", []string{"node"}, nil),
		devicesTotalDesc: prometheus.NewDesc("homelab_disk_devices_total", "Total discovered disk devices", []string{"node"}, nil),
	}

	return c
}

func (c *diskCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.infoDesc
	ch <- c.sizeDesc
	ch <- c.tempDesc
	ch <- c.usedDesc
	ch <- c.spareDesc
	ch <- c.pohDesc
	ch <- c.readDesc
	ch <- c.writtenDesc
	ch <- c.unsafeDesc
	ch <- c.mediaErrDesc
	ch <- c.critWarnDesc
	ch <- c.scrapeDurDesc
	ch <- c.devicesTotalDesc
}

func (c *diskCollector) Collect(ch chan<- prometheus.Metric) {
	start := time.Now()
	disks := discoverDisks()
	osdMap := buildOSDMap()

	for i := range disks {
		if osd, ok := osdMap[disks[i].Device]; ok {
			disks[i].CephOSD = osd
		}
		readSMART(&disks[i])
	}

	scrapeD := time.Since(start).Seconds()
	log.Printf("collected: %d devices in %.2fs", len(disks), scrapeD)

	for _, d := range disks {
		osdStr := strconv.Itoa(d.CephOSD)
		ch <- prometheus.MustNewConstMetric(c.infoDesc, prometheus.GaugeValue, 1,
			c.nodeName, d.Device, d.Model, d.Serial, d.Firmware, d.Transport, d.Rotational, osdStr)
		ch <- prometheus.MustNewConstMetric(c.sizeDesc, prometheus.GaugeValue, d.SizeBytes, c.nodeName, d.Device)

		if d.HasSMART {
			ch <- prometheus.MustNewConstMetric(c.tempDesc, prometheus.GaugeValue, d.Temperature, c.nodeName, d.Device)
			ch <- prometheus.MustNewConstMetric(c.usedDesc, prometheus.GaugeValue, d.PercentageUsed, c.nodeName, d.Device)
			ch <- prometheus.MustNewConstMetric(c.spareDesc, prometheus.GaugeValue, d.AvailableSpare, c.nodeName, d.Device)
			ch <- prometheus.MustNewConstMetric(c.pohDesc, prometheus.GaugeValue, d.PowerOnHours, c.nodeName, d.Device)
			ch <- prometheus.MustNewConstMetric(c.readDesc, prometheus.GaugeValue, d.DataReadBytes, c.nodeName, d.Device)
			ch <- prometheus.MustNewConstMetric(c.writtenDesc, prometheus.GaugeValue, d.DataWrittenBytes, c.nodeName, d.Device)
			ch <- prometheus.MustNewConstMetric(c.unsafeDesc, prometheus.GaugeValue, d.UnsafeShutdowns, c.nodeName, d.Device)
			ch <- prometheus.MustNewConstMetric(c.mediaErrDesc, prometheus.GaugeValue, d.MediaErrors, c.nodeName, d.Device)
			ch <- prometheus.MustNewConstMetric(c.critWarnDesc, prometheus.GaugeValue, d.CriticalWarning, c.nodeName, d.Device)
		}
	}

	ch <- prometheus.MustNewConstMetric(c.scrapeDurDesc, prometheus.GaugeValue, scrapeD, c.nodeName)
	ch <- prometheus.MustNewConstMetric(c.devicesTotalDesc, prometheus.GaugeValue, float64(len(disks)), c.nodeName)
}

// --- Device Discovery ---

var skipDeviceRe = regexp.MustCompile(`^(loop|dm-|ram|nbd|sr|rbd)`)

func discoverDisks() []diskInfo {
	entries, err := os.ReadDir(hostSysBlock)
	if err != nil {
		log.Printf("error reading %s: %v", hostSysBlock, err)
		return nil
	}

	var disks []diskInfo
	for _, e := range entries {
		name := e.Name()
		if skipDeviceRe.MatchString(name) {
			continue
		}

		d := diskInfo{
			Device:  name,
			CephOSD: -1,
		}

		base := filepath.Join(hostSysBlock, name)

		d.Model = readSysfs(filepath.Join(base, "device", "model"))
		d.Serial = readSysfs(filepath.Join(base, "device", "serial"))
		d.Firmware = readSysfs(filepath.Join(base, "device", "firmware_rev"))
		d.Rotational = readSysfs(filepath.Join(base, "queue", "rotational"))

		if sizeStr := readSysfs(filepath.Join(base, "size")); sizeStr != "" {
			if sectors, err := strconv.ParseFloat(sizeStr, 64); err == nil {
				d.SizeBytes = sectors * 512
			}
		}

		// Determine transport type
		if strings.HasPrefix(name, "nvme") {
			d.Transport = "nvme"
		} else {
			d.Transport = "sata"
		}

		disks = append(disks, d)
	}

	return disks
}

func readSysfs(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// --- Ceph OSD Mapping ---

type osdEntry struct {
	osdID   int
	modTime time.Time
}

func buildOSDMap() map[string]int {
	candidates := make(map[string]osdEntry) // device -> best candidate

	entries, err := os.ReadDir(hostRookPath)
	if err != nil {
		log.Printf("no rook data at %s: %v", hostRookPath, err)
		return nil
	}

	for _, e := range entries {
		if !e.IsDir() || !strings.Contains(e.Name(), "_") {
			continue
		}

		dirPath := filepath.Join(hostRookPath, e.Name())

		// Read OSD ID from whoami
		whoamiData, err := os.ReadFile(filepath.Join(dirPath, "whoami"))
		if err != nil {
			continue
		}
		osdID, err := strconv.Atoi(strings.TrimSpace(string(whoamiData)))
		if err != nil {
			continue
		}

		// Read block symlink to get device path
		blockLink, err := os.Readlink(filepath.Join(dirPath, "block"))
		if err != nil {
			continue
		}

		// Get directory mod time to resolve conflicts (stale vs active OSD)
		info, err := e.Info()
		if err != nil {
			continue
		}

		devName := filepath.Base(blockLink)
		devName = stripPartition(devName)

		// When multiple rook dirs map to the same device, prefer the most recent
		if existing, ok := candidates[devName]; !ok || info.ModTime().After(existing.modTime) {
			candidates[devName] = osdEntry{osdID: osdID, modTime: info.ModTime()}
		}
	}

	m := make(map[string]int, len(candidates))
	for dev, entry := range candidates {
		m[dev] = entry.osdID
		log.Printf("OSD mapping: %s -> osd.%d", dev, entry.osdID)
	}
	return m
}

// stripPartition removes partition suffix: nvme0n1p1 -> nvme0n1, sda1 -> sda
func stripPartition(dev string) string {
	// NVMe: nvme0n1p1 -> nvme0n1
	if strings.HasPrefix(dev, "nvme") {
		if idx := strings.LastIndex(dev, "p"); idx > 0 {
			suffix := dev[idx+1:]
			if _, err := strconv.Atoi(suffix); err == nil {
				// Check this isn't the "n1" part by verifying there's "n" before
				before := dev[:idx]
				if strings.Contains(before, "n") {
					return before
				}
			}
		}
		return dev
	}
	// SATA: sda1 -> sda, sdb2 -> sdb
	re := regexp.MustCompile(`^(sd[a-z]+)\d+$`)
	if m := re.FindStringSubmatch(dev); m != nil {
		return m[1]
	}
	return dev
}

// --- NVMe SMART via ioctl ---

func readNVMeSMART(d *diskInfo) bool {
	// Determine controller char device: nvme0n1 -> /host/dev/nvme0
	ctrlDev := nvmeControllerDevice(d.Device)
	if ctrlDev == "" {
		return false
	}

	fd, err := unix.Open(ctrlDev, unix.O_RDONLY, 0)
	if err != nil {
		log.Printf("cannot open %s: %v", ctrlDev, err)
		return false
	}
	defer unix.Close(fd)

	buf := make([]byte, smartLogSize)

	cmd := nvmeAdminCommand{
		Opcode:  nvmeGetLogPage,
		Nsid:    0xFFFFFFFF,
		Addr:    uint64(uintptr(unsafe.Pointer(&buf[0]))),
		DataLen: smartLogSize,
		Cdw10:   uint32(smartLogID) | (((smartLogSize / 4) - 1) << 16),
	}

	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(nvmeAdminCmd), uintptr(unsafe.Pointer(&cmd)))
	if errno != 0 {
		log.Printf("NVMe ioctl failed on %s: %v", ctrlDev, errno)
		return false
	}

	// Parse SMART log page (NVMe spec 1.4+)
	d.CriticalWarning = float64(buf[0])
	// Temperature: bytes 1-2, little-endian Kelvin
	tempK := float64(binary.LittleEndian.Uint16(buf[1:3]))
	d.Temperature = tempK - 273.15
	d.AvailableSpare = float64(buf[3])
	d.PercentageUsed = float64(buf[5])

	// Data units read: bytes 32-47, uint128 LE (each unit = 512KB = 512000 bytes per spec)
	d.DataReadBytes = readUint128AsFloat(buf[32:48]) * 512000
	// Data units written: bytes 48-63
	d.DataWrittenBytes = readUint128AsFloat(buf[48:64]) * 512000
	// Power-on hours: bytes 128-143
	d.PowerOnHours = readUint128AsFloat(buf[128:144])
	// Unsafe shutdowns: bytes 144-159
	d.UnsafeShutdowns = readUint128AsFloat(buf[144:160])
	// Media errors: bytes 160-175
	d.MediaErrors = readUint128AsFloat(buf[160:176])

	d.HasSMART = true
	return true
}

// nvmeControllerDevice maps "nvme0n1" -> "/host/dev/nvme0"
func nvmeControllerDevice(dev string) string {
	if !strings.HasPrefix(dev, "nvme") {
		return ""
	}
	// Find the "n" that separates controller from namespace
	idx := strings.Index(dev[4:], "n")
	if idx < 0 {
		return ""
	}
	ctrlName := dev[:4+idx] // e.g., "nvme0"
	return filepath.Join(hostDevPath, ctrlName)
}

// readUint128AsFloat reads a 16-byte little-endian uint128 as float64
// We only need the lower 64 bits for practical values, but handle overflow
func readUint128AsFloat(b []byte) float64 {
	lo := binary.LittleEndian.Uint64(b[0:8])
	hi := binary.LittleEndian.Uint64(b[8:16])
	if hi > 0 {
		return float64(hi)*18446744073709551616.0 + float64(lo)
	}
	return float64(lo)
}

// --- SATA SMART via smartctl ---

type smartctlOutput struct {
	Temperature struct {
		Current int `json:"current"`
	} `json:"temperature"`
	ATASmartAttributes struct {
		Table []struct {
			ID    int    `json:"id"`
			Name  string `json:"name"`
			Value int    `json:"value"`
			Raw   struct {
				Value int `json:"value"`
			} `json:"raw"`
		} `json:"table"`
	} `json:"ata_smart_attributes"`
	PowerOnTime struct {
		Hours int `json:"hours"`
	} `json:"power_on_time"`
}

func readSATASMART(d *diskInfo) bool {
	devPath := filepath.Join(hostDevPath, d.Device)
	// Use -d sat since device paths are under /host/dev/ and auto-detection fails
	// smartctl often exits non-zero even with valid data (bit-mask exit codes)
	out, err := exec.Command("smartctl", "-j", "-a", "-d", "sat", devPath).CombinedOutput()
	if err != nil && len(out) == 0 {
		log.Printf("smartctl failed for %s: %v", d.Device, err)
		return false
	}

	var result smartctlOutput
	if err := json.Unmarshal(out, &result); err != nil {
		log.Printf("smartctl JSON parse error for %s: %v", d.Device, err)
		return false
	}

	d.Temperature = float64(result.Temperature.Current)
	d.PowerOnHours = float64(result.PowerOnTime.Hours)

	// Parse ATA SMART attributes
	for _, attr := range result.ATASmartAttributes.Table {
		switch attr.ID {
		case 174: // Unexpect_Power_Loss_Ct
			d.UnsafeShutdowns = float64(attr.Raw.Value)
		case 202: // Percent_Lifetime_Remain (Crucial/Micron convention: value = % remaining)
			d.PercentageUsed = 100 - float64(attr.Raw.Value)
		case 241, 246: // Total_LBAs_Written (241 standard, 246 Crucial/Micron)
			d.DataWrittenBytes = float64(attr.Raw.Value) * 512
		case 242: // Total_LBAs_Read
			d.DataReadBytes = float64(attr.Raw.Value) * 512
		}
	}

	d.HasSMART = true
	return true
}

// --- Combined SMART reader ---

func readSMART(d *diskInfo) {
	if d.Transport == "nvme" {
		readNVMeSMART(d)
	} else {
		readSATASMART(d)
	}
}

// --- Main ---

func main() {
	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		nodeName = "unknown"
	}

	listenAddr := os.Getenv("LISTEN_ADDR")
	if listenAddr == "" {
		listenAddr = ":9999"
	}

	log.Printf("starting homelab-disk-exporter on %s, listen=%s", nodeName, listenAddr)

	collector := newDiskCollector(nodeName)
	prometheus.MustRegister(collector)

	http.Handle("/metrics", promhttp.Handler())
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})

	log.Fatal(http.ListenAndServe(listenAddr, nil))
}
