package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// syncGost wires GOST to this admin-api (auth/limiter/observer plugins) and to
// the broker (outbound chain) using the GOST Web API. It is best-effort: each
// object is created/updated independently and failures are reported but do not
// stop the service. You can also apply deploy/gost.yaml by hand instead.
//
// Env:
//
//	GOST_API_URL   e.g. http://gost-proxy:18080   (GOST Web API base)
//	GOST_API_USER / GOST_API_PASS                 (Web API basic auth)
//	ADMIN_SELF_URL e.g. http://admin-api:8088      (how GOST reaches us)
//	BROKER_SOCKS   e.g. broker:1080                (broker SOCKS front)
//	GOST_SERVICE   service name to update          (default "service-0")
//	GOST_LISTEN    customer listen addr            (default ":8080")
func syncGost() error {
	base := os.Getenv("GOST_API_URL")
	self := envOr("ADMIN_SELF_URL", "http://admin-api:8088")
	broker := envOr("BROKER_SOCKS", "broker:1080")
	svc := envOr("GOST_SERVICE", "service-0")
	listen := envOr("GOST_LISTEN", ":8080")

	c := &gostClient{base: base, user: os.Getenv("GOST_API_USER"), pass: os.Getenv("GOST_API_PASS")}

	// 1) plugins
	auther := map[string]any{"name": "auther-0",
		"plugin": map[string]any{"type": "http", "addr": self + "/gost/auth", "timeout": "5s"}}
	limiter := map[string]any{"name": "limiter-0",
		"plugin": map[string]any{"type": "http", "addr": self + "/gost/limiter", "timeout": "5s"}}
	observer := map[string]any{"name": "observer-0",
		"plugin": map[string]any{"type": "http", "addr": self + "/gost/observer", "timeout": "5s"}}
	// 2) outbound chain to the broker's SOCKS5 front
	chain := map[string]any{"name": "chain-0", "hops": []any{
		map[string]any{"name": "hop-0", "nodes": []any{
			map[string]any{"name": "node-0", "addr": broker,
				"connector": map[string]any{"type": "socks5"},
				"dialer":    map[string]any{"type": "tcp"}},
		}},
	}}
	// 3) service that uses them
	service := map[string]any{
		"name": svc, "addr": listen,
		"handler": map[string]any{"type": "http", "auther": "auther-0",
			"limiter": "limiter-0", "chain": "chain-0"},
		"observer": "observer-0",
		"listener": map[string]any{"type": "tcp"},
		"metadata": map[string]any{"enableStats": true, "observePeriod": "10s"},
	}

	var firstErr error
	note := func(label string, err error) {
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("%s: %w", label, err)
		}
	}
	note("authers", c.upsert("authers", "auther-0", auther))
	note("limiters", c.upsert("limiters", "limiter-0", limiter))
	note("observers", c.upsert("observers", "observer-0", observer))
	note("chains", c.upsert("chains", "chain-0", chain))
	note("service", c.put("services", svc, service))
	return firstErr
}

type gostClient struct {
	base, user, pass string
}

func (g *gostClient) do(method, path string, body any) error {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, g.base+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if g.user != "" {
		req.SetBasicAuth(g.user, g.pass)
	}
	cl := &http.Client{Timeout: 8 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s %s -> %d: %s", method, path, resp.StatusCode, string(b))
	}
	return nil
}

// upsert tries POST (create); if the object already exists, falls back to PUT.
func (g *gostClient) upsert(kind, name string, body any) error {
	err := g.do(http.MethodPost, "/config/"+kind, body)
	if err == nil {
		return nil
	}
	return g.do(http.MethodPut, "/config/"+kind+"/"+name, body)
}

func (g *gostClient) put(kind, name string, body any) error {
	// PUT updates an existing service (restarts it); fall back to POST create.
	if err := g.do(http.MethodPut, "/config/"+kind+"/"+name, body); err == nil {
		return nil
	}
	return g.do(http.MethodPost, "/config/"+kind, body)
}
