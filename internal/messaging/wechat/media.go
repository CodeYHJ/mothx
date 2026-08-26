package wechat

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const wechatMediaDownloadTimeout = time.Minute

// OpenCDNMedia opens the event-authenticated content represented by media.
// iLink media is AES-128-ECB encrypted in observed clients. The decrypting
// reader intentionally streams into the Runtime attachment store, so the
// channel adapter never owns a local media file or unbounded byte slice.
func (c *Client) OpenCDNMedia(ctx context.Context, media CDNMedia, aesKeyOverride string) (io.ReadCloser, error) {
	if c == nil || c.HTTP == nil {
		return nil, fmt.Errorf("wechat media client is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	downloadURL, err := wechatMediaURL(media)
	if err != nil {
		return nil, err
	}
	requestCtx, cancel := withMediaTimeout(ctx)
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, downloadURL, nil)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create wechat media download request: %w", err)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("download wechat media: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("download wechat media: HTTP %d", resp.StatusCode)
	}

	source := &cancelReadCloser{ReadCloser: resp.Body, cancel: cancel}
	keySource := strings.TrimSpace(aesKeyOverride)
	if keySource == "" {
		keySource = strings.TrimSpace(media.AESKey)
	}
	// Some observed iLink events omit encryption metadata. Preserve the bytes
	// instead of inventing a key; Runtime still performs its normal size and
	// image content validation before the input reaches an Agent.
	if keySource == "" {
		return source, nil
	}
	key, err := DecodeAESKey(keySource)
	if err != nil {
		source.Close()
		return nil, fmt.Errorf("decode wechat media AES key: %w", err)
	}
	return newAESECBDecryptReadCloser(source, key)
}

func withMediaTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, wechatMediaDownloadTimeout)
}

func wechatMediaURL(media CDNMedia) (string, error) {
	if fullURL := strings.TrimSpace(media.FullURL); fullURL != "" {
		parsed, err := url.Parse(fullURL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return "", fmt.Errorf("invalid WeChat media download URL")
		}
		return parsed.String(), nil
	}
	if strings.TrimSpace(media.EncryptQueryParam) == "" {
		return "", fmt.Errorf("wechat media has no download reference")
	}
	return CDNBaseURL + "/download?encrypted_query_param=" + url.QueryEscape(media.EncryptQueryParam), nil
}

type cancelReadCloser struct {
	io.ReadCloser
	cancel func()
	once   sync.Once
}

func (r *cancelReadCloser) Close() error {
	err := r.ReadCloser.Close()
	r.once.Do(r.cancel)
	return err
}

// aesECBDecryptReadCloser decrypts AES-ECB and validates PKCS#7 padding while
// retaining only one final block. It is deliberately a reader rather than a
// []byte helper: AttachmentService enforces its normal accepted-byte cap while
// data is copied into private Runtime storage.
type aesECBDecryptReadCloser struct {
	source     io.ReadCloser
	block      cipher.Block
	ciphertext []byte
	plaintext  []byte
	lastBlock  []byte
	eof        bool
	finished   bool
	err        error
}

func newAESECBDecryptReadCloser(source io.ReadCloser, key []byte) (io.ReadCloser, error) {
	if source == nil {
		return nil, fmt.Errorf("wechat media source is nil")
	}
	if len(key) != 16 {
		return nil, fmt.Errorf("AES key must be 16 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return &aesECBDecryptReadCloser{source: source, block: block}, nil
}

func (r *aesECBDecryptReadCloser) Read(dst []byte) (int, error) {
	if len(dst) == 0 {
		return 0, nil
	}
	for len(r.plaintext) == 0 && !r.finished {
		if r.err != nil {
			r.finished = true
			return 0, r.err
		}
		if r.eof {
			if err := r.finish(); err != nil {
				r.finished = true
				return 0, err
			}
			continue
		}

		buf := make([]byte, 32*1024)
		n, err := r.source.Read(buf)
		if n > 0 {
			r.ciphertext = append(r.ciphertext, buf[:n]...)
			if processErr := r.processBlocks(); processErr != nil {
				r.err = processErr
			}
		}
		if err == io.EOF {
			r.eof = true
		} else if err != nil {
			r.err = err
		}
		if n == 0 && err == nil {
			continue
		}
	}
	if len(r.plaintext) == 0 {
		return 0, io.EOF
	}
	n := copy(dst, r.plaintext)
	r.plaintext = r.plaintext[n:]
	return n, nil
}

func (r *aesECBDecryptReadCloser) processBlocks() error {
	blockSize := r.block.BlockSize()
	for len(r.ciphertext) >= blockSize {
		cipherBlock := r.ciphertext[:blockSize]
		r.ciphertext = r.ciphertext[blockSize:]
		plainBlock := make([]byte, blockSize)
		r.block.Decrypt(plainBlock, cipherBlock)
		if r.lastBlock != nil {
			r.plaintext = append(r.plaintext, r.lastBlock...)
		}
		r.lastBlock = plainBlock
	}
	return nil
}

func (r *aesECBDecryptReadCloser) finish() error {
	if r.finished {
		return nil
	}
	r.finished = true
	if len(r.ciphertext) != 0 {
		return fmt.Errorf("wechat media ciphertext length is not a multiple of AES block size")
	}
	if len(r.lastBlock) == 0 {
		return fmt.Errorf("wechat media ciphertext is empty")
	}
	padding := int(r.lastBlock[len(r.lastBlock)-1])
	if padding == 0 || padding > len(r.lastBlock) {
		return fmt.Errorf("wechat media has invalid PKCS7 padding")
	}
	for _, b := range r.lastBlock[len(r.lastBlock)-padding:] {
		if int(b) != padding {
			return fmt.Errorf("wechat media has invalid PKCS7 padding")
		}
	}
	r.plaintext = append(r.plaintext, r.lastBlock[:len(r.lastBlock)-padding]...)
	r.lastBlock = nil
	return nil
}

func (r *aesECBDecryptReadCloser) Close() error {
	return r.source.Close()
}
