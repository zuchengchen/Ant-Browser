package keypouch

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Device communicates only with the configured KeyPouch origin. The deep link
// contains an opaque ticket; it cannot change the destination of a signed request.
type Device struct {
	stateDir string
	origin   string
	key      ed25519.PrivateKey
	id       string
	http     *http.Client
}

func NewDevice(origin string, seed []byte) (*Device, error) {
	u, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("KeyPouch 站点地址无效")
	}
	loopback := u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback()
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return nil, errors.New("KeyPouch 站点需要 HTTPS")
	}
	if len(seed) != ed25519.SeedSize {
		return nil, errors.New("KeyPouch 设备密钥无效")
	}
	key := ed25519.NewKeyFromSeed(seed)
	digest := sha256.Sum256(key.Public().(ed25519.PublicKey))
	return &Device{
		origin: u.Scheme + "://" + u.Host,
		key:    key,
		id:     base64.RawURLEncoding.EncodeToString(digest[:]),
		http: &http.Client{
			Timeout:       10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

func (d *Device) Fingerprint() string {
	h := sha256.Sum256(d.key.Public().(ed25519.PublicKey))
	digits := strings.ToUpper(hex.EncodeToString(h[:5]))
	return digits[:5] + "-" + digits[5:]
}

func (d *Device) Offer(ctx context.Context, pairID, label string) error {
	var response struct {
		ID          string `json:"device_id"`
		Fingerprint string `json:"fingerprint"`
	}
	err := d.request(ctx, "/api/ant/device/pair", map[string]string{
		"pair_id": pairID, "public_key": base64.RawURLEncoding.EncodeToString(d.key.Public().(ed25519.PublicKey)), "label": label,
	}, &response, false)
	if err == nil && (response.ID != d.id || response.Fingerprint != d.Fingerprint()) {
		return errors.New("KeyPouch 配对回执与本机身份不匹配")
	}
	return err
}

func (d *Device) Post(ctx context.Context, path string, input, output any) error {
	if !strings.HasPrefix(path, "/api/ant/device/") {
		return errors.New("KeyPouch 设备接口无效")
	}
	return d.request(ctx, path, input, output, true)
}

func (d *Device) request(ctx context.Context, path string, input, output any, signed bool) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.origin+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if signed {
		var n [24]byte
		if _, err := rand.Read(n[:]); err != nil {
			return err
		}
		nonce := base64.RawURLEncoding.EncodeToString(n[:])
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		hash := sha256.Sum256(body)
		msg := strings.Join([]string{"ANT1", ts, nonce, req.Method, req.URL.EscapedPath(), hex.EncodeToString(hash[:])}, "\n")
		req.Header.Set("X-Ant-Device", d.id)
		req.Header.Set("X-Ant-Time", ts)
		req.Header.Set("X-Ant-Nonce", nonce)
		req.Header.Set("X-Ant-Signature", base64.RawURLEncoding.EncodeToString(ed25519.Sign(d.key, []byte(msg))))
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return errors.New("无法连接 KeyPouch 站点")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("KeyPouch 返回 HTTP %d", resp.StatusCode)
	}
	if output == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return err
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(output)
}
