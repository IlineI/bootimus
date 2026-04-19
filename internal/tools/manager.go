package tools

import (
	"archive/zip"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"bootimus/internal/models"
	"bootimus/internal/storage"
)

// ToolDefinition defines a built-in tool that can be downloaded and served
type ToolDefinition struct {
	Name        string
	DisplayName string
	Description string
	Version     string
	DownloadURL string // default download URL (user can override in DB)
	// iPXE boot config
	KernelPath string // path within extracted dir
	InitrdPath string // path within extracted dir (empty for single-binary tools)
	BootParams string // kernel boot parameters (use {{HTTP_URL}} for server URL)
	BootMethod string // "kernel" (default), "memdisk", or "chain"
	ArchiveType string // "zip" (default), "bin" (single binary download), "iso"
}

var BuiltInTools = []ToolDefinition{
	{
		Name:        "gparted",
		DisplayName: "GParted Live",
		Description: "Partition editor for managing disk partitions",
		Version:     "1.8.1-2",
		DownloadURL: "https://downloads.sourceforge.net/project/gparted/gparted-live-stable/1.8.1-2/gparted-live-1.8.1-2-amd64.zip",
		KernelPath:  "live/vmlinuz",
		InitrdPath:  "live/initrd.img",
		BootParams:  "boot=live config components union=overlay username=user noswap noeject vga=788 fetch={{HTTP_URL}}/tools/gparted/live/filesystem.squashfs",
	},
	{
		Name:        "clonezilla",
		DisplayName: "Clonezilla Live",
		Description: "Disk cloning and imaging tool",
		Version:     "3.2.2-5",
		DownloadURL: "https://downloads.sourceforge.net/project/clonezilla/clonezilla_live_stable/3.2.2-5/clonezilla-live-3.2.2-5-amd64.zip",
		KernelPath:  "live/vmlinuz",
		InitrdPath:  "live/initrd.img",
		BootParams:  "boot=live config components union=overlay username=user noswap noeject vga=788 fetch={{HTTP_URL}}/tools/clonezilla/live/filesystem.squashfs",
	},
	{
		Name:        "memtest86plus",
		DisplayName: "Memtest86+",
		Description: "Memory testing and diagnostics",
		Version:     "7.20",
		DownloadURL: "https://memtest.org/download/v7.20/mt86plus_7.20.binaries.zip",
		KernelPath:  "memtest64.bin",
		InitrdPath:  "",
		BootParams:  "",
		BootMethod:  "kernel",
		ArchiveType: "zip",
	},
	{
		Name:        "systemrescue",
		DisplayName: "SystemRescue",
		Description: "Linux rescue toolkit for file recovery, disk repair, and network tools",
		Version:     "13.00",
		DownloadURL: "https://downloads.sourceforge.net/project/systemrescuecd/sysresccd-x86/13.00/systemrescue-13.00-amd64.iso",
		KernelPath:  "sysresccd/boot/x86_64/vmlinuz",
		InitrdPath:  "sysresccd/boot/intel_ucode.img",
		BootParams:  "archisobasedir=sysresccd archiso_http_srv={{HTTP_URL}}/tools/systemrescue/ checksum ip=dhcp",
		ArchiveType: "iso",
	},
	{
		Name:        "shredos",
		DisplayName: "ShredOS",
		Description: "Secure disk wiping based on nwipe",
		Version:     "2025.11_29_0.40",
		DownloadURL: "https://github.com/PartialVolume/shredos.x86_64/releases/download/v2025.11_29_x86-64_0.40/shredos-2025.11_29_x86-64_v0.40_20260402.img",
		KernelPath:  "shredos.img",
		InitrdPath:  "",
		BootParams:  "",
		BootMethod:  "memdisk",
		ArchiveType: "bin",
	},
	{
		Name:        "netbootxyz",
		DisplayName: "Netboot.xyz",
		Description: "Chainload into netboot.xyz for hundreds of OS installers and tools",
		Version:     "2.0.84",
		DownloadURL: "https://boot.netboot.xyz/ipxe/netboot.xyz.efi",
		KernelPath:  "netboot.xyz.efi",
		InitrdPath:  "",
		BootParams:  "",
		BootMethod:  "chain",
		ArchiveType: "bin",
	},
}

type DownloadProgress struct {
	Status     string  `json:"status"`      // "idle", "downloading", "extracting", "done", "error"
	Percent    float64 `json:"percent"`
	Downloaded int64   `json:"downloaded"`
	Total      int64   `json:"total"`
	Error      string  `json:"error,omitempty"`
}

type Manager struct {
	store      storage.Storage
	dataDir    string
	progressMu sync.RWMutex
	progress   map[string]*DownloadProgress
}

func NewManager(store storage.Storage, dataDir string) *Manager {
	return &Manager{store: store, dataDir: dataDir, progress: make(map[string]*DownloadProgress)}
}

func (m *Manager) GetProgress(name string) DownloadProgress {
	m.progressMu.RLock()
	defer m.progressMu.RUnlock()
	if p, ok := m.progress[name]; ok {
		return *p
	}
	return DownloadProgress{Status: "idle"}
}

func (m *Manager) setProgress(name string, p *DownloadProgress) {
	m.progressMu.Lock()
	defer m.progressMu.Unlock()
	m.progress[name] = p
}

func (m *Manager) ToolsDir() string {
	return filepath.Join(m.dataDir, "tools")
}

func (m *Manager) ToolDir(name string) string {
	return filepath.Join(m.ToolsDir(), name)
}

// GetDefinition returns the built-in definition for a tool
func GetDefinition(name string) *ToolDefinition {
	for i := range BuiltInTools {
		if BuiltInTools[i].Name == name {
			return &BuiltInTools[i]
		}
	}
	return nil
}

// EnsureToolRecords makes sure all built-in tools have database records
func (m *Manager) EnsureToolRecords() error {
	for _, def := range BuiltInTools {
		existing, err := m.store.GetBootTool(def.Name)
		if err != nil {
			// Create new record with default URL
			tool := &models.BootTool{
				Name:        def.Name,
				DisplayName: def.DisplayName,
				Description: def.Description,
				Version:     def.Version,
				Enabled:     false,
				Downloaded:  m.IsDownloaded(def.Name),
				DownloadURL: def.DownloadURL,
			}
			if err := m.store.SaveBootTool(tool); err != nil {
				return fmt.Errorf("failed to create tool record for %s: %w", def.Name, err)
			}
		} else {
			// Update display info but preserve user's custom URL if set
			existing.DisplayName = def.DisplayName
			existing.Description = def.Description
			existing.Downloaded = m.IsDownloaded(def.Name)
			if existing.DownloadURL == "" {
				existing.DownloadURL = def.DownloadURL
			}
			m.store.SaveBootTool(existing)
		}
	}
	return nil
}

// IsDownloaded checks if a tool's files exist on disk
func (m *Manager) IsDownloaded(name string) bool {
	var kernelPath, initrdPath string

	if def := GetDefinition(name); def != nil {
		kernelPath = def.KernelPath
		initrdPath = def.InitrdPath
	} else if tool, err := m.store.GetBootTool(name); err == nil && tool.Custom {
		kernelPath = tool.KernelPath
		initrdPath = tool.InitrdPath
	} else {
		return false
	}

	if kernelPath == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(m.ToolDir(name), kernelPath)); err != nil {
		return false
	}
	if initrdPath != "" {
		if _, err := os.Stat(filepath.Join(m.ToolDir(name), initrdPath)); err != nil {
			return false
		}
	}
	return true
}

// Download downloads and extracts a tool's files. Uses the URL from the DB record.
func (m *Manager) Download(name string, progressCh chan<- string) error {
	tool, err := m.store.GetBootTool(name)
	if err != nil {
		return fmt.Errorf("tool not found in database: %w", err)
	}

	// Build effective definition from built-in or custom tool DB fields
	var displayName, downloadURL, kernelPath, archiveType string

	if def := GetDefinition(name); def != nil {
		displayName = def.DisplayName
		downloadURL = tool.DownloadURL
		if downloadURL == "" {
			downloadURL = def.DownloadURL
		}
		kernelPath = def.KernelPath
		archiveType = def.ArchiveType
	} else if tool.Custom {
		displayName = tool.DisplayName
		downloadURL = tool.DownloadURL
		kernelPath = tool.KernelPath
		archiveType = tool.ArchiveType
	} else {
		return fmt.Errorf("unknown tool: %s", name)
	}

	if downloadURL == "" {
		return fmt.Errorf("no download URL configured for %s", name)
	}

	toolDir := m.ToolDir(name)
	if err := os.MkdirAll(toolDir, 0755); err != nil {
		return fmt.Errorf("failed to create tool directory: %w", err)
	}

	log.Printf("Tools: Downloading %s from %s", displayName, downloadURL)

	tmpFile, err := os.CreateTemp("", "bootimus-tool-*.zip")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	req, err := http.NewRequest("GET", downloadURL, nil)
	if err != nil {
		tmpFile.Close()
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", "Bootimus PXE Server")

	client := &http.Client{
		Timeout: 30 * time.Minute,
	}
	resp, err := client.Do(req)
	if err != nil {
		tmpFile.Close()
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		tmpFile.Close()
		m.setProgress(name, &DownloadProgress{Status: "error", Error: fmt.Sprintf("HTTP %d", resp.StatusCode)})
		return fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	totalSize := resp.ContentLength
	prog := &DownloadProgress{Status: "downloading", Total: totalSize}
	m.setProgress(name, prog)

	pw := &progressWriter{
		writer: tmpFile,
		onProgress: func(n int64) {
			prog.Downloaded = n
			if totalSize > 0 {
				prog.Percent = float64(n) / float64(totalSize) * 100
			}
			m.setProgress(name, prog)
		},
	}

	written, err := io.Copy(pw, resp.Body)
	tmpFile.Close()
	if err != nil {
		m.setProgress(name, &DownloadProgress{Status: "error", Error: err.Error()})
		return fmt.Errorf("download write failed: %w", err)
	}

	log.Printf("Tools: Downloaded %s (%d bytes)", displayName, written)
	m.setProgress(name, &DownloadProgress{Status: "extracting", Percent: 100, Downloaded: written, Total: totalSize})

	// Handle different archive types
	if archiveType == "" {
		archiveType = "zip"
	}

	switch archiveType {
	case "zip":
		if err := extractZip(tmpPath, toolDir); err != nil {
			m.setProgress(name, &DownloadProgress{Status: "error", Error: err.Error()})
			return fmt.Errorf("zip extraction failed: %w", err)
		}
	case "bin":
		// Single binary - move to tool dir with the expected filename
		destName := kernelPath
		if destName == "" {
			destName = filepath.Base(downloadURL)
		}
		destPath := filepath.Join(toolDir, destName)
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			m.setProgress(name, &DownloadProgress{Status: "error", Error: err.Error()})
			return fmt.Errorf("failed to create directory: %w", err)
		}
		if err := copyFile(tmpPath, destPath); err != nil {
			m.setProgress(name, &DownloadProgress{Status: "error", Error: err.Error()})
			return fmt.Errorf("failed to copy binary: %w", err)
		}
	case "iso":
		// Copy the ISO and extract it
		isoPath := filepath.Join(toolDir, name+".iso")
		if err := copyFile(tmpPath, isoPath); err != nil {
			m.setProgress(name, &DownloadProgress{Status: "error", Error: err.Error()})
			return fmt.Errorf("failed to copy ISO: %w", err)
		}
	default:
		m.setProgress(name, &DownloadProgress{Status: "error", Error: "unknown archive type: " + archiveType})
		return fmt.Errorf("unknown archive type: %s", archiveType)
	}

	// Update database
	tool, err = m.store.GetBootTool(name)
	if err != nil {
		return err
	}
	tool.Downloaded = true
	if err := m.store.SaveBootTool(tool); err != nil {
		return err
	}

	log.Printf("Tools: %s ready at %s", displayName, toolDir)
	m.setProgress(name, &DownloadProgress{Status: "done", Percent: 100, Downloaded: written, Total: totalSize})

	return nil
}

// Delete removes a tool's downloaded files
func (m *Manager) Delete(name string) error {
	toolDir := m.ToolDir(name)
	if err := os.RemoveAll(toolDir); err != nil {
		return err
	}

	tool, err := m.store.GetBootTool(name)
	if err != nil {
		return err
	}
	tool.Downloaded = false
	tool.Enabled = false
	return m.store.SaveBootTool(tool)
}

// GetEnabledTools returns tools that are enabled and downloaded
func (m *Manager) GetEnabledTools(serverURL string) []EnabledTool {
	tools, err := m.store.ListBootTools()
	if err != nil {
		return nil
	}

	var result []EnabledTool
	for _, tool := range tools {
		if !tool.Enabled || !tool.Downloaded {
			continue
		}

		var kp, ip, bp, bm string

		if def := GetDefinition(tool.Name); def != nil {
			kp = def.KernelPath
			ip = def.InitrdPath
			bp = def.BootParams
			bm = def.BootMethod
		} else if tool.Custom {
			kp = tool.KernelPath
			ip = tool.InitrdPath
			bp = tool.BootParams
			bm = tool.BootMethod
		} else {
			continue
		}

		if kp == "" {
			continue
		}

		params := strings.ReplaceAll(bp, "{{HTTP_URL}}", serverURL)
		if bm == "" {
			bm = "kernel"
		}
		et := EnabledTool{
			Name:        tool.Name,
			DisplayName: tool.DisplayName,
			KernelURL:   fmt.Sprintf("%s/tools/%s/%s", serverURL, tool.Name, kp),
			BootParams:  params,
			BootMethod:  bm,
		}
		if ip != "" {
			et.InitrdURL = fmt.Sprintf("%s/tools/%s/%s", serverURL, tool.Name, ip)
		}
		result = append(result, et)
	}
	return result
}

type EnabledTool struct {
	Name        string
	DisplayName string
	KernelURL   string
	InitrdURL   string
	BootParams  string
	BootMethod  string // "kernel", "memdisk", "chain"
}

type progressWriter struct {
	writer     io.Writer
	written    int64
	onProgress func(int64)
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n, err := pw.writer.Write(p)
	pw.written += int64(n)
	if pw.onProgress != nil {
		pw.onProgress(pw.written)
	}
	return n, err
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func extractZip(zipPath, destDir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		target := filepath.Join(destDir, f.Name)

		// Security: prevent zip slip
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(destDir)+string(os.PathSeparator)) {
			continue
		}

		if f.FileInfo().IsDir() {
			os.MkdirAll(target, 0755)
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}

		outFile, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			return err
		}

		rc, err := f.Open()
		if err != nil {
			outFile.Close()
			return err
		}

		_, err = io.Copy(outFile, rc)
		rc.Close()
		outFile.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
