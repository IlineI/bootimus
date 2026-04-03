package extractor

import (
	"fmt"
	"log"
	"strings"
)

// detectGenericUnified scans the entire ISO filesystem for kernel and initrd
// files by matching common naming patterns, without relying on distro-specific
// hardcoded paths. This is the fallback when no known distro layout matches.
func (e *Extractor) detectGenericUnified(reader FileSystemReader) (*BootFiles, error) {
	log.Printf("Generic boot file scanner: scanning ISO filesystem...")

	var kernelCandidates []string
	var initrdCandidates []string

	// Recursively walk the filesystem
	walkDir(reader, "/", 0, 5, func(path string, isDir bool) {
		if isDir {
			return
		}
		lower := strings.ToLower(path)
		name := lower
		if idx := strings.LastIndex(lower, "/"); idx >= 0 {
			name = lower[idx+1:]
		}

		// Kernel patterns
		if isKernelFile(name) {
			kernelCandidates = append(kernelCandidates, path)
		}

		// Initrd patterns
		if isInitrdFile(name) {
			initrdCandidates = append(initrdCandidates, path)
		}
	})

	log.Printf("Generic scanner found %d kernel candidates: %v", len(kernelCandidates), kernelCandidates)
	log.Printf("Generic scanner found %d initrd candidates: %v", len(initrdCandidates), initrdCandidates)

	if len(kernelCandidates) == 0 {
		return nil, fmt.Errorf("no kernel files found by generic scanner")
	}
	if len(initrdCandidates) == 0 {
		return nil, fmt.Errorf("kernel found but no initrd files found by generic scanner")
	}

	// Pick the best pair: prefer files in the same directory
	kernel, initrd := pickBestPair(kernelCandidates, initrdCandidates)

	log.Printf("Generic scanner selected: kernel=%s initrd=%s", kernel, initrd)

	// Try to guess boot params from the directory structure
	bootParams := guessBootParams(reader, kernel)

	return &BootFiles{
		Kernel:     kernel,
		Initrd:     initrd,
		Distro:     "generic",
		BootParams: bootParams,
	}, nil
}

func isKernelFile(name string) bool {
	kernelPatterns := []string{
		"vmlinuz",
		"bzimage",
		"linux",
	}
	for _, p := range kernelPatterns {
		if name == p || strings.HasPrefix(name, p+"-") || strings.HasPrefix(name, p+".") {
			return true
		}
	}
	return false
}

func isInitrdFile(name string) bool {
	initrdPatterns := []string{
		"initrd",
		"initramfs",
	}
	for _, p := range initrdPatterns {
		if name == p || strings.HasPrefix(name, p+"-") || strings.HasPrefix(name, p+".") {
			return true
		}
	}
	return false
}

// walkDir recursively walks a filesystem reader up to maxDepth levels deep.
func walkDir(reader FileSystemReader, path string, depth, maxDepth int, fn func(path string, isDir bool)) {
	if depth > maxDepth {
		return
	}

	entries, err := reader.ListDirectory(path)
	if err != nil {
		return
	}

	for _, entry := range entries {
		// Skip hidden/special entries
		if entry.Name == "" || entry.Name == "." || entry.Name == ".." {
			continue
		}

		fullPath := path
		if fullPath == "/" {
			fullPath = "/" + entry.Name
		} else {
			fullPath = path + "/" + entry.Name
		}

		fn(fullPath, entry.IsDir)

		if entry.IsDir {
			walkDir(reader, fullPath, depth+1, maxDepth, fn)
		}
	}
}

// pickBestPair selects the best kernel/initrd combination.
// Prefers pairs that are in the same directory.
func pickBestPair(kernels, initrds []string) (string, string) {
	// First pass: find a pair in the same directory
	for _, k := range kernels {
		kDir := parentDir(k)
		for _, i := range initrds {
			if parentDir(i) == kDir {
				return k, i
			}
		}
	}
	// Fallback: just use the first of each
	return kernels[0], initrds[0]
}

func parentDir(path string) string {
	if idx := strings.LastIndex(path, "/"); idx > 0 {
		return path[:idx]
	}
	return "/"
}

// guessBootParams tries to infer appropriate boot parameters based on
// what else is on the ISO (casper, live, arch layout, etc.)
func guessBootParams(reader FileSystemReader, kernelPath string) string {
	lower := strings.ToLower(kernelPath)

	// Casper-based (Ubuntu family)
	if strings.Contains(lower, "/casper/") {
		return "boot=casper root=/dev/ram0 ramdisk_size=1500000 "
	}

	// Live systems
	if strings.Contains(lower, "/live/") {
		return "boot=live "
	}

	// Arch-based: check for airootfs or archiso marker
	if reader.FileExists("/arch/x86_64/airootfs.sfs") ||
		reader.FileExists("/arch/boot/syslinux/archiso_sys-linux.cfg") {
		return "archisobasedir=arch "
	}

	// Check for syslinux configs that might contain boot params
	// Common in many distros
	syslinuxPaths := []string{
		"/boot/syslinux/syslinux.cfg",
		"/syslinux/syslinux.cfg",
		"/isolinux/isolinux.cfg",
		"/boot/grub/grub.cfg",
	}
	for _, cfgPath := range syslinuxPaths {
		content := reader.ReadFileContent(cfgPath)
		if content != "" {
			if params := extractBootParamsFromConfig(content, kernelPath); params != "" {
				return params
			}
		}
	}

	return ""
}

// extractBootParamsFromConfig tries to find the APPEND/linux line for the
// kernel in a syslinux/grub config and extract the boot parameters.
func extractBootParamsFromConfig(config, kernelPath string) string {
	kernelName := kernelPath
	if idx := strings.LastIndex(kernelPath, "/"); idx >= 0 {
		kernelName = kernelPath[idx+1:]
	}

	lines := strings.Split(config, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(strings.ToLower(line))

		// syslinux: look for KERNEL/LINUX line matching our kernel, then APPEND on next lines
		if (strings.HasPrefix(trimmed, "kernel ") || strings.HasPrefix(trimmed, "linux ")) &&
			strings.Contains(trimmed, strings.ToLower(kernelName)) {
			// Look at subsequent lines for APPEND
			for j := i + 1; j < len(lines) && j < i+5; j++ {
				appendLine := strings.TrimSpace(lines[j])
				if strings.HasPrefix(strings.ToLower(appendLine), "append ") {
					// Extract everything after "APPEND " but remove initrd= parts
					params := appendLine[7:]
					params = removeInitrdParam(params)
					return strings.TrimSpace(params) + " "
				}
			}
		}

		// grub: look for linux line with our kernel
		if strings.HasPrefix(trimmed, "linux ") && strings.Contains(trimmed, strings.ToLower(kernelName)) {
			parts := strings.Fields(line)
			if len(parts) > 2 {
				// Everything after "linux /path/to/kernel" is boot params
				params := strings.Join(parts[2:], " ")
				params = removeInitrdParam(params)
				return strings.TrimSpace(params) + " "
			}
		}
	}

	return ""
}

// removeInitrdParam strips initrd=... from a parameter string since we handle
// initrd separately.
func removeInitrdParam(params string) string {
	var result []string
	for _, p := range strings.Fields(params) {
		if !strings.HasPrefix(strings.ToLower(p), "initrd=") {
			result = append(result, p)
		}
	}
	return strings.Join(result, " ")
}
