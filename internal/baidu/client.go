package baidu

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBaseURL    = "https://pan.baidu.com"
	defaultPCSBaseURL = "https://pcs.baidu.com"
	defaultUserAgent  = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36"
	panAppID          = "250528"
)

type ClientOption func(*clientOptions)

type clientOptions struct {
	baseURL        string
	pcsBaseURL     string
	httpClient     *http.Client
	sleep          func(context.Context, time.Duration) error
	now            func() time.Time
	maxListRetries int
}

type Client struct {
	baseURL        *url.URL
	pcsBaseURL     *url.URL
	httpClient     *http.Client
	sleep          func(context.Context, time.Duration) error
	now            func() time.Time
	maxListRetries int
}

func WithBaseURL(raw string) ClientOption {
	return func(options *clientOptions) { options.baseURL = raw }
}

func WithPCSBaseURL(raw string) ClientOption {
	return func(options *clientOptions) { options.pcsBaseURL = raw }
}

func WithHTTPClient(client *http.Client) ClientOption {
	return func(options *clientOptions) { options.httpClient = client }
}

func WithSleep(fn func(context.Context, time.Duration) error) ClientOption {
	return func(options *clientOptions) { options.sleep = fn }
}

func NewClient(cookieHeader string, opts ...ClientOption) (*Client, error) {
	options := clientOptions{
		baseURL:        defaultBaseURL,
		pcsBaseURL:     defaultPCSBaseURL,
		now:            time.Now,
		maxListRetries: 4,
		sleep: func(ctx context.Context, d time.Duration) error {
			timer := time.NewTimer(d)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		},
	}
	for _, opt := range opts {
		opt(&options)
	}
	base, err := parseServiceBase(options.baseURL, "Baidu base")
	if err != nil {
		return nil, err
	}
	pcsBase, err := parseServiceBase(options.pcsBaseURL, "Baidu PCS base")
	if err != nil {
		return nil, err
	}
	jar, err := newCookieJar(base, cookieHeader)
	if err != nil {
		return nil, fmt.Errorf("create Baidu cookie jar: %w", err)
	}
	httpClient := options.httpClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 45 * time.Second}
	}
	httpClient.Jar = jar
	return &Client{
		baseURL:        base,
		pcsBaseURL:     pcsBase,
		httpClient:     httpClient,
		sleep:          options.sleep,
		now:            options.now,
		maxListRetries: options.maxListRetries,
	}, nil
}

func parseServiceBase(raw, label string) (*url.URL, error) {
	base, err := url.Parse(raw)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("invalid %s URL", label)
	}
	return base, nil
}

func (c *Client) CookieString() string {
	if c == nil || c.httpClient == nil || c.httpClient.Jar == nil {
		return ""
	}
	return cookieString(c.httpClient.Jar.Cookies(c.baseURL))
}

func (c *Client) HasLoginCookies() bool {
	if c == nil || c.httpClient == nil || c.httpClient.Jar == nil {
		return false
	}
	foundBDUSS := false
	foundSTOKEN := false
	for _, cookie := range c.httpClient.Jar.Cookies(c.baseURL) {
		switch strings.ToUpper(cookie.Name) {
		case "BDUSS":
			foundBDUSS = cookie.Value != ""
		case "STOKEN":
			foundSTOKEN = cookie.Value != ""
		}
	}
	return foundBDUSS && foundSTOKEN
}

func (c *Client) AccessSharePage(ctx context.Context, link ShareLink) (ShareContext, error) {
	body, status, err := c.do(ctx, http.MethodGet, "/s/"+url.PathEscape(link.Feature), nil, nil, "https://pan.baidu.com/disk/home", 16<<20)
	if err != nil {
		return ShareContext{}, err
	}
	if status == http.StatusNotFound || strings.Contains(string(body), "platform-non-found") || strings.Contains(string(body), "error-404") {
		return ShareContext{}, ErrShareExpired
	}
	if status < 200 || status >= 400 {
		return ShareContext{}, fmt.Errorf("Baidu share page returned HTTP %d", status)
	}
	shareContext, err := extractShareContext(body)
	if err != nil {
		return ShareContext{}, err
	}
	return shareContext, nil
}

func (c *Client) VerifyPassword(ctx context.Context, link ShareLink, share ShareContext, password string) error {
	if strings.TrimSpace(password) == "" {
		return ErrPasswordRequired
	}
	query := url.Values{}
	query.Set("shareid", share.ShareID)
	query.Set("t", strconv.FormatInt(c.now().UnixMilli(), 10))
	query.Set("clienttype", "1")
	query.Set("uk", share.ShareUK)
	form := url.Values{}
	form.Set("pwd", password)
	form.Set("vcode", "null")
	form.Set("vcode_str", "null")
	form.Set("bdstoken", share.BDSToken)
	body, status, err := c.do(ctx, http.MethodPost, "/share/verify", query, form, link.SanitizedURL(), 2<<20)
	if err != nil {
		return err
	}
	if status < 200 || status >= 400 {
		return fmt.Errorf("Baidu password verification returned HTTP %d", status)
	}
	var response struct {
		Errno int `json:"errno"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return fmt.Errorf("parse Baidu password verification response: %w", err)
	}
	switch response.Errno {
	case 0:
		return nil
	case -9:
		return ErrWrongPassword
	case -6, -7:
		return ErrAuthRequired
	case 8001, -62:
		return ErrVerificationRequired
	default:
		return &RemoteError{Operation: "password verification", Errno: response.Errno}
	}
}

func (c *Client) do(ctx context.Context, method, endpoint string, query, form url.Values, referer string, maxBytes int64) ([]byte, int, error) {
	return c.doAt(ctx, c.baseURL, method, endpoint, query, form, referer, maxBytes)
}

func (c *Client) doPCS(ctx context.Context, method, endpoint string, query, form url.Values, maxBytes int64) ([]byte, int, error) {
	return c.doAt(ctx, c.pcsBaseURL, method, endpoint, query, form, "", maxBytes)
}

func (c *Client) doAt(ctx context.Context, base *url.URL, method, endpoint string, query, form url.Values, referer string, maxBytes int64) ([]byte, int, error) {
	reference := &url.URL{Path: endpoint}
	u := base.ResolveReference(reference)
	if query != nil {
		u.RawQuery = query.Encode()
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	request, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, 0, fmt.Errorf("create Baidu request: %w", err)
	}
	request.Header.Set("User-Agent", defaultUserAgent)
	request.Header.Set("Accept", "application/json,text/html;q=0.9,*/*;q=0.8")
	if form != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	}
	if referer != "" {
		request.Header.Set("Referer", referer)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, 0, fmt.Errorf("Baidu request failed: %w", err)
	}
	defer response.Body.Close()
	data, err := readLimited(response.Body, maxBytes)
	if err != nil {
		return nil, response.StatusCode, err
	}
	return data, response.StatusCode, nil
}

func readLimited(reader io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("invalid response size limit")
	}
	limited := io.LimitReader(reader, maxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read Baidu response: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("Baidu response exceeded %d bytes", maxBytes)
	}
	return data, nil
}
