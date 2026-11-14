// Package graph talks to Microsoft Graph over plain HTTP (no Graph SDK: that
// SDK is huge and very slow to compile). It handles cloud endpoints, service
// principal authentication, paging and throttling detection.
package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
)

// endpoint holds everything that differs between Azure clouds.
type endpoint struct {
	authority cloud.Configuration // Entra login host (login.microsoftonline.com / .cn / .us)
	baseURL   string              // Graph API base URL
	scope     string              // token scope for Graph
}

var endpoints = map[string]endpoint{
	"public": {cloud.AzurePublic, "https://graph.microsoft.com/v1.0", "https://graph.microsoft.com/.default"},
	"china":  {cloud.AzureChina, "https://microsoftgraph.chinacloudapi.cn/v1.0", "https://microsoftgraph.chinacloudapi.cn/.default"},
	"usgov":  {cloud.AzureGovernment, "https://graph.microsoft.us/v1.0", "https://graph.microsoft.us/.default"},
	// TODO: the US DoD cloud uses https://dod-graph.microsoft.us; add an entry if you need it.
}

type Options struct {
	Cloud        string // public | china | usgov
	TenantID     string
	ClientID     string
	ClientSecret string
}

type Client struct {
	http    *http.Client
	cred    azcore.TokenCredential
	baseURL string
	scope   string
}

// New creates the service principal credential and the Graph HTTP client for the chosen cloud.
func New(o Options) (*Client, error) {
	ep, ok := endpoints[strings.ToLower(o.Cloud)]
	if !ok {
		return nil, fmt.Errorf("unknown cloud %q (use public, china or usgov)", o.Cloud)
	}
	cred, err := azidentity.NewClientSecretCredential(o.TenantID, o.ClientID, o.ClientSecret,
		&azidentity.ClientSecretCredentialOptions{ClientOptions: azcore.ClientOptions{Cloud: ep.authority}})
	if err != nil {
		return nil, fmt.Errorf("create credential: %w", err)
	}
	return &Client{
		http:    &http.Client{Timeout: 2 * time.Minute},
		cred:    cred,
		baseURL: ep.baseURL,
		scope:   ep.scope,
	}, nil
}

// CheckAccess fetches one Graph token so wrong tenant/client/secret/cloud
// show up at startup instead of on the first scrape.
// (It proves the credentials work, not that Application.Read.All is consented.)
func (c *Client) CheckAccess(ctx context.Context) error {
	_, err := c.cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{c.scope}})
	return err
}

// ThrottledError is returned when Graph answers 429 (Too Many Requests).
type ThrottledError struct{ RetryAfter time.Duration }

func (e *ThrottledError) Error() string {
	return fmt.Sprintf("graph throttled the request (retry after %s)", e.RetryAfter)
}

// IsThrottled reports whether err is (or wraps) a ThrottledError.
func IsThrottled(err error) bool {
	var te *ThrottledError
	return errors.As(err, &te)
}

// getJSON performs an authenticated GET and decodes the JSON body into out.
// The token is cached by azidentity, so calling GetToken per request is cheap.
func (c *Client) getJSON(ctx context.Context, url string, out any) error {
	tok, err := c.cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{c.scope}})
	if err != nil {
		return fmt.Errorf("get token: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	req.Header.Set("Accept", "application/json")

	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	switch {
	case res.StatusCode == http.StatusTooManyRequests:
		secs, _ := strconv.Atoi(res.Header.Get("Retry-After"))
		return &ThrottledError{RetryAfter: time.Duration(secs) * time.Second}
	case res.StatusCode != http.StatusOK:
		body, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return fmt.Errorf("graph returned %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(res.Body).Decode(out)
}

// listAll follows @odata.nextLink until every page of a collection is read.
// path is relative to the Graph base URL, e.g. "/applications?$top=999".
func listAll[T any](ctx context.Context, c *Client, path string) ([]T, error) {
	type page struct {
		Value    []T    `json:"value"`
		NextLink string `json:"@odata.nextLink"`
	}
	var all []T
	next := c.baseURL + path
	for next != "" {
		var p page
		if err := c.getJSON(ctx, next, &p); err != nil {
			return nil, err
		}
		all = append(all, p.Value...)
		next = p.NextLink
	}
	return all, nil
}
