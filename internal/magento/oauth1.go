// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" // #nosec G505 -- OAuth 1.0a mandates HMAC-SHA1; protocol requirement, not password hashing
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

const (
	oauthSignatureMethod = "HMAC-SHA1"
	oauthVersion         = "1.0"
	formContentType      = "application/x-www-form-urlencoded"
	oauthUnreserved      = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"
	upperHex             = "0123456789ABCDEF"
)

// OAuth1Signer signs Magento requests with OAuth 1.0a HMAC-SHA1 (RFC 5849).
type OAuth1Signer struct {
	consumerKey    string
	consumerSecret string
	token          string
	tokenSecret    string
	nonceSource    func() (string, int64)
}

// NewOAuth1Signer returns a signer for the four integration credentials.
func NewOAuth1Signer(consumerKey, consumerSecret, token, tokenSecret string) (*OAuth1Signer, error) {
	if consumerKey == "" || consumerSecret == "" {
		return nil, invalidf("Magento OAuth 1.0a requires a consumer key and consumer secret")
	}
	if (token == "") != (tokenSecret == "") {
		return nil, invalidf("Magento OAuth 1.0a token and token secret must both be set or both be empty")
	}
	return &OAuth1Signer{consumerKey: consumerKey, consumerSecret: consumerSecret, token: token, tokenSecret: tokenSecret, nonceSource: newNonce}, nil
}

// SetNonceSource replaces the nonce/timestamp generator; nil restores the default.
func (s *OAuth1Signer) SetNonceSource(f func() (string, int64)) {
	if s == nil {
		return
	}
	if f == nil {
		f = newNonce
	}
	s.nonceSource = f
}

// Sign implements Signer.
func (s *OAuth1Signer) Sign(req *http.Request) error {
	base, params, err := s.prepare(req)
	if err != nil {
		return err
	}
	params["oauth_signature"] = s.signature(base)
	req.Header.Set("Authorization", oauthAuthorization(params))
	return nil
}

// prepare computes the RFC 5849 signature base string and OAuth parameters.
func (s *OAuth1Signer) prepare(req *http.Request) (string, map[string]string, error) {
	if req == nil || req.URL == nil {
		return "", nil, invalidf("Magento OAuth 1.0a needs an HTTP request")
	}
	if s == nil || s.nonceSource == nil {
		return "", nil, errs.New(errs.CodeInternal, "Magento OAuth 1.0a signer is not configured")
	}
	nonce, timestamp := s.nonceSource()
	oauth := map[string]string{
		"oauth_consumer_key": s.consumerKey, "oauth_nonce": nonce, "oauth_signature_method": oauthSignatureMethod,
		"oauth_timestamp": strconv.FormatInt(timestamp, 10), "oauth_version": oauthVersion,
	}
	if s.token != "" {
		oauth["oauth_token"] = s.token
	}
	values, err := requestParameters(req)
	if err != nil {
		return "", nil, err
	}
	type pair struct{ key, value string }
	pairs := make([]pair, 0, len(oauth)+8)
	for key, vals := range values {
		for _, value := range vals {
			pairs = append(pairs, pair{key: key, value: value})
		}
	}
	for key, value := range oauth {
		pairs = append(pairs, pair{key: key, value: value})
	}
	sort.Slice(pairs, func(i, j int) bool {
		ki, kj := percentEncode(pairs[i].key), percentEncode(pairs[j].key)
		if ki != kj {
			return ki < kj
		}
		return percentEncode(pairs[i].value) < percentEncode(pairs[j].value)
	})
	var normalized strings.Builder
	for i, p := range pairs {
		if i > 0 {
			normalized.WriteByte('&')
		}
		normalized.WriteString(percentEncode(p.key) + "=" + percentEncode(p.value))
	}
	base := strings.ToUpper(req.Method) + "&" + percentEncode(requestBaseURI(req.URL)) + "&" + percentEncode(normalized.String())
	return base, oauth, nil
}
func (s *OAuth1Signer) signature(base string) string {
	mac := hmac.New(sha1.New, []byte(percentEncode(s.consumerSecret)+"&"+percentEncode(s.tokenSecret)))
	mac.Write([]byte(base))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
func newNonce() (string, int64) {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf), time.Now().Unix()
}

// requestParameters collects query and form-body parameters for the signature.
func requestParameters(req *http.Request) (url.Values, error) {
	values := make(url.Values)
	for key, vals := range req.URL.Query() {
		values[key] = append(values[key], vals...)
	}
	contentType := strings.ToLower(req.Header.Get("Content-Type"))
	if req.Body == nil || !strings.HasPrefix(contentType, formContentType) {
		return values, nil
	}
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, errs.New(errs.CodeInternal, "Magento request body could not be read for signing").WithCause(err)
	}
	req.Body = io.NopCloser(bytes.NewReader(raw))
	form, err := url.ParseQuery(string(raw))
	if err != nil {
		return nil, invalidf("Magento form body could not be parsed for signing")
	}
	for key, vals := range form {
		values[key] = append(values[key], vals...)
	}
	return values, nil
}
func requestBaseURI(u *url.URL) string {
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) + path
}
func percentEncode(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if strings.IndexByte(oauthUnreserved, c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(upperHex[c>>4])
		b.WriteByte(upperHex[c&0x0f])
	}
	return b.String()
}
func oauthAuthorization(params map[string]string) string {
	parts := make([]string, 0, len(params))
	for _, key := range []string{"oauth_consumer_key", "oauth_nonce", "oauth_signature_method", "oauth_timestamp", "oauth_token", "oauth_version", "oauth_signature"} {
		if value, ok := params[key]; ok {
			parts = append(parts, percentEncode(key)+`="`+percentEncode(value)+`"`)
		}
	}
	return "OAuth " + strings.Join(parts, ", ")
}

// BearerSigner authenticates requests with an integration access token.
type BearerSigner struct {
	Token string
}

// Sign implements Signer.
func (s BearerSigner) Sign(req *http.Request) error {
	if req == nil {
		return invalidf("Magento bearer signer needs an HTTP request")
	}
	if s.Token == "" {
		return errs.New(errs.CodeUnauthenticated, "Magento bearer token is not configured")
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	return nil
}
