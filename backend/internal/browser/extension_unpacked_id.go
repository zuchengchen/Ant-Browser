package browser

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"unicode/utf16"
)

// UnpackedExtensionID uses Chromium's runtime identity, which can differ from
// Ant's catalog ID. An extension without a manifest key keeps its existing
// origin as long as it is loaded from the same directory.
func UnpackedExtensionID(dir string) (string, error) {
	if !filepath.IsAbs(dir) {
		return "", errors.New("扩展目录需要绝对路径")
	}
	var manifest struct {
		Key string `json:"key"`
	}
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return "", err
	}
	if manifest.Key != "" {
		key, err := base64.StdEncoding.DecodeString(manifest.Key)
		if err != nil {
			return "", err
		}
		return extensionIDFromPublicKey(key), nil
	}

	// UnpackedInstaller calls MakeAbsoluteFilePath. POSIX resolves symlinks;
	// Windows uses GetFullPathName and hashes the UTF-16 path bytes.
	path := filepath.Clean(dir)
	if runtime.GOOS != "windows" {
		path, err = filepath.EvalSymlinks(path)
		if err != nil {
			return "", err
		}
	}
	return unpackedExtensionPathID(path, runtime.GOOS == "windows"), nil
}

func unpackedExtensionPathID(path string, windows bool) string {
	data := []byte(path)
	if windows {
		if len(path) >= 2 && path[1] == ':' && path[0] >= 'a' && path[0] <= 'z' {
			path = string(path[0]-'a'+'A') + path[1:]
		}
		chars := utf16.Encode([]rune(path))
		data = make([]byte, 2*len(chars))
		for i, char := range chars {
			binary.LittleEndian.PutUint16(data[2*i:], char)
		}
	}
	hash := sha256.Sum256(data)
	return extensionIDFromBytes(hash[:16])
}
