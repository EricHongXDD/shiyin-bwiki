package download

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"
)

const maxFileNameBytes = 200

var windowsReservedNames = map[string]struct{}{
	"CON": {}, "PRN": {}, "AUX": {}, "NUL": {},
	"COM1": {}, "COM2": {}, "COM3": {}, "COM4": {}, "COM5": {}, "COM6": {}, "COM7": {}, "COM8": {}, "COM9": {},
	"LPT1": {}, "LPT2": {}, "LPT3": {}, "LPT4": {}, "LPT5": {}, "LPT6": {}, "LPT7": {}, "LPT8": {}, "LPT9": {},
}

// SanitizeFileName 将外部提供的名称转换为单个安全文件名。
func SanitizeFileName(name string) string {
	name = strings.TrimSpace(name)
	var builder strings.Builder
	for _, character := range name {
		switch {
		case character < 0x20 || character == 0x7f:
			builder.WriteRune('_')
		case strings.ContainsRune(`<>:"/\|?*`, character):
			builder.WriteRune('_')
		default:
			builder.WriteRune(character)
		}
	}
	name = strings.TrimRight(builder.String(), " .")
	if name == "" || name == "." || name == ".." {
		name = "download"
	}

	extension := filepath.Ext(name)
	stem := strings.TrimSuffix(name, extension)
	if _, reserved := windowsReservedNames[strings.ToUpper(stem)]; reserved {
		stem = "_" + stem
		name = stem + extension
	}
	if len(name) > maxFileNameBytes {
		extension = truncateUTF8(extension, 32)
		available := maxFileNameBytes - len(extension)
		if available < 1 {
			available = maxFileNameBytes
			extension = ""
		}
		stem = truncateUTF8(stem, available)
		name = strings.TrimRight(stem, " .") + extension
		if name == "" {
			name = "download"
		}
	}
	return name
}

func truncateUTF8(value string, maximum int) string {
	if maximum <= 0 {
		return ""
	}
	if len(value) <= maximum {
		return value
	}
	for maximum > 0 && !utf8.ValidString(value[:maximum]) {
		maximum--
	}
	return value[:maximum]
}

func chooseAvailablePath(directory, fileName string, reserved map[string]struct{}) (string, string, error) {
	originalExtension := filepath.Ext(fileName)
	extension := truncateUTF8(originalExtension, 32)
	stem := strings.TrimSuffix(fileName, originalExtension)
	for suffix := 0; suffix < 100000; suffix++ {
		candidate := fileName
		if suffix > 0 {
			marker := fmt.Sprintf(" (%d)", suffix)
			maximumStemBytes := maxFileNameBytes - len(marker) - len(extension)
			candidateStem := strings.TrimRight(truncateUTF8(stem, maximumStemBytes), " .")
			if candidateStem == "" {
				candidateStem = "download"
			}
			candidate = candidateStem + marker + extension
		}
		outputPath := filepath.Join(directory, candidate)
		if _, exists := reserved[pathKey(outputPath)]; exists {
			continue
		}
		occupied, err := pathOccupied(outputPath)
		if err != nil {
			return "", "", err
		}
		if occupied {
			continue
		}
		return candidate, outputPath, nil
	}
	return "", "", errors.New("无法为下载文件生成不重复的名称")
}

func pathOccupied(outputPath string) (bool, error) {
	for _, target := range []string{outputPath, outputPath + ".part"} {
		_, err := os.Lstat(target)
		switch {
		case err == nil:
			return true, nil
		case os.IsNotExist(err):
			continue
		default:
			return false, fmt.Errorf("检查目标文件 %q：%w", target, err)
		}
	}
	return false, nil
}

func pathKey(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}
