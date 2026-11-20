package keypouch

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
)

// OpenDevice loads the private identity for this OS user, Ant installation and
// KeyPouch site. Only an explicit pairing request may create a new identity.
func OpenDevice(configDir, installation, site string, create bool) (*Device, error) {
	h := sha256.Sum256([]byte(installation + "\x00" + site))
	dir := filepath.Join(configDir, "Ant Browser", "KeyPouch", hex.EncodeToString(h[:16]))
	path := filepath.Join(dir, "device")
	read := func() (*Device, error) {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		seed, err := unprotectDeviceSeed(b)
		if err != nil {
			return nil, err
		}
		d, err := NewDevice(site, seed)
		if err == nil {
			d.stateDir = dir
		}
		return d, err
	}
	d, err := read()
	if !create || !errors.Is(err, os.ErrNotExist) {
		return d, err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	var seed [ed25519.SeedSize]byte
	if _, err := rand.Read(seed[:]); err != nil {
		return nil, err
	}
	d, err = NewDevice(site, seed[:])
	if err != nil {
		return nil, err
	}
	d.stateDir = dir
	data, err := protectDeviceSeed(seed[:])
	if err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(dir, ".identity-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	// Install a complete key without replacing one saved by another launch.
	err = os.Link(tmp.Name(), path)
	if errors.Is(err, os.ErrExist) {
		return read()
	}
	return d, err
}
