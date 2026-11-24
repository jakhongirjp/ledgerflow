package httpapi_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jakhongirjp/ledgerflow/internal/httpapi"
	"github.com/jakhongirjp/ledgerflow/internal/ledger"
	"github.com/jakhongirjp/ledgerflow/internal/store/memory"
)

type client struct {
	t   *testing.T
	srv *httptest.Server
}

func newClient(t *testing.T) *client {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := httpapi.New(ledger.NewService(memory.New(), nil), logger, httpapi.NewMetrics())
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &client{t: t, srv: srv}
}

func (c *client) do(method, path string, body any, headers map[string]string) (*http.Response, map[string]any) {
	c.t.Helper()
	var buf io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		buf = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.srv.URL+path, buf)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func (c *client) account(typ string, allowNeg bool) string {
	resp, body := c.do("POST", "/v1/accounts", map[string]any{
		"name": typ, "type": typ, "currency": "USD", "allow_negative": allowNeg,
	}, nil)
	if resp.StatusCode != http.StatusCreated {
		c.t.Fatalf("create account: %d %v", resp.StatusCode, body)
	}
	return body["id"].(string)
}

func entries(debit, credit string, amount int64) map[string]any {
	return map[string]any{
		"description": "test",
		"entries": []map[string]any{
			{"account_id": debit, "direction": "debit", "amount": amount},
			{"account_id": credit, "direction": "credit", "amount": amount},
		},
	}
}

func TestTransactionLifecycle(t *testing.T) {
	c := newClient(t)
	bank := c.account("asset", true)
	wallet := c.account("liability", false)

	resp, body := c.do("POST", "/v1/transactions", entries(bank, wallet, 500), map[string]string{"Idempotency-Key": "dep-1"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("post: %d %v", resp.StatusCode, body)
	}
	txID := body["id"].(string)

	resp, body = c.do("POST", "/v1/transactions", entries(bank, wallet, 500), map[string]string{"Idempotency-Key": "dep-1"})
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Idempotent-Replayed") != "true" || body["id"] != txID {
		t.Fatalf("replay: %d %v", resp.StatusCode, body)
	}

	resp, body = c.do("GET", "/v1/accounts/"+wallet, nil, nil)
	if resp.StatusCode != http.StatusOK || body["balance"].(float64) != 500 {
		t.Fatalf("wallet: %d %v", resp.StatusCode, body)
	}

	resp, body = c.do("GET", "/v1/transactions/"+txID, nil, nil)
	if resp.StatusCode != http.StatusOK || body["currency"] != "USD" {
		t.Fatalf("get tx: %d %v", resp.StatusCode, body)
	}

	resp, body = c.do("GET", "/v1/accounts/"+wallet+"/postings?limit=5", nil, nil)
	if resp.StatusCode != http.StatusOK || len(body["data"].([]any)) != 1 {
		t.Fatalf("postings: %d %v", resp.StatusCode, body)
	}
}

func TestErrorMapping(t *testing.T) {
	c := newClient(t)
	bank := c.account("asset", true)
	wallet := c.account("liability", false)
	other := c.account("liability", false)
	key := map[string]string{"Idempotency-Key": "k1"}
	c.do("POST", "/v1/transactions", entries(bank, wallet, 100), map[string]string{"Idempotency-Key": "seed"})

	tests := []struct {
		name    string
		method  string
		path    string
		body    any
		headers map[string]string
		status  int
		typ     string
	}{
		{"missing key", "POST", "/v1/transactions", entries(bank, wallet, 1), nil, 400, "missing_idempotency_key"},
		{"unbalanced", "POST", "/v1/transactions", map[string]any{"entries": []map[string]any{
			{"account_id": bank, "direction": "debit", "amount": 2},
			{"account_id": wallet, "direction": "credit", "amount": 1},
		}}, key, 422, "validation_failed"},
		{"overdraft", "POST", "/v1/transactions", entries(wallet, other, 101), key, 422, "insufficient_funds"},
		{"unknown field", "POST", "/v1/accounts", map[string]any{"name": "x", "colour": "red"}, nil, 400, "invalid_json"},
		{"bad uuid", "GET", "/v1/accounts/nope", nil, nil, 400, "invalid_id"},
		{"missing account", "GET", "/v1/accounts/00000000-0000-0000-0000-000000000000", nil, nil, 404, "not_found"},
		{"bad currency", "POST", "/v1/accounts", map[string]any{"name": "x", "type": "asset", "currency": "dollars"}, nil, 422, "validation_failed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := c.do(tc.method, tc.path, tc.body, tc.headers)
			if resp.StatusCode != tc.status || body["type"] != "urn:ledgerflow:"+tc.typ {
				t.Fatalf("got %d %v, want %d %s", resp.StatusCode, body, tc.status, tc.typ)
			}
			if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
				t.Fatalf("content-type %q", ct)
			}
		})
	}

	// Same key, different body -> 409.
	c.do("POST", "/v1/transactions", entries(bank, wallet, 1), map[string]string{"Idempotency-Key": "dup"})
	resp, body := c.do("POST", "/v1/transactions", entries(bank, wallet, 2), map[string]string{"Idempotency-Key": "dup"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("conflict: %d %v", resp.StatusCode, body)
	}
}

func TestOperationalEndpoints(t *testing.T) {
	c := newClient(t)
	for _, p := range []string{"/healthz", "/readyz"} {
		if resp, _ := c.do("GET", p, nil, nil); resp.StatusCode != http.StatusNoContent {
			t.Fatalf("%s: %d", p, resp.StatusCode)
		}
	}
	c.account("asset", false)
	resp, err := http.Get(c.srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), `ledgerflow_http_requests_total{route="POST /v1/accounts",status="201"} 1`) {
		t.Fatalf("metrics missing request counter:\n%s", b)
	}
}

func TestRequestIDIsPropagated(t *testing.T) {
	c := newClient(t)
	resp, _ := c.do("GET", "/healthz", nil, map[string]string{"X-Request-Id": "abc-123"})
	if resp.Header.Get("X-Request-Id") != "abc-123" {
		t.Fatalf("got %q", resp.Header.Get("X-Request-Id"))
	}
	resp, _ = c.do("GET", "/healthz", nil, map[string]string{"X-Request-Id": "bad id with spaces"})
	if id := resp.Header.Get("X-Request-Id"); id == "" || strings.Contains(id, " ") {
		t.Fatalf("unsafe id echoed: %q", id)
	}
}
