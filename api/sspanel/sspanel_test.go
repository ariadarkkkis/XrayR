package sspanel_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ariadarkkkis/XrayR/api"
	"github.com/ariadarkkkis/XrayR/api/sspanel"
)

const (
	fixtureKey    = "fixture-api-key"
	fixtureNodeID = 41
)

type recordedRequest struct {
	method string
	path   string
	query  url.Values
	etag   string
	body   []byte
}

type contractServer struct {
	t        *testing.T
	requests []recordedRequest
	gets     map[string]int
}

func newContractServer(t *testing.T) (*contractServer, *httptest.Server) {
	t.Helper()

	recorder := &contractServer{t: t, gets: make(map[string]int)}
	server := httptest.NewServer(http.HandlerFunc(recorder.serveHTTP))
	t.Cleanup(server.Close)
	return recorder, server
}

func (s *contractServer) serveHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	require.NoError(s.t, err)
	s.requests = append(s.requests, recordedRequest{
		method: r.Method,
		path:   r.URL.Path,
		query:  r.URL.Query(),
		etag:   r.Header.Get("If-None-Match"),
		body:   body,
	})

	if r.Method == http.MethodGet {
		s.gets[r.URL.Path]++
		if s.gets[r.URL.Path] > 1 {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/mod_mu/nodes/41/info":
		w.Header().Set("ETag", `"node-fixture-v1"`)
		s.writeFixture(w, "node.json")
	case "/mod_mu/users":
		w.Header().Set("ETag", `"users-fixture-v1"`)
		s.writeFixture(w, "users.json")
	case "/mod_mu/func/detect_rules":
		w.Header().Set("ETag", `"rules-fixture-v1"`)
		s.writeFixture(w, "detection-rules.json")
	case "/mod_mu/users/traffic", "/mod_mu/users/aliveip", "/mod_mu/users/detectlog":
		_, err := w.Write([]byte(`{"ret":1,"data":{}}`))
		require.NoError(s.t, err)
	default:
		http.Error(w, "unexpected route", http.StatusNotFound)
	}
}

func (s *contractServer) writeFixture(w http.ResponseWriter, name string) {
	s.t.Helper()
	fixture, err := os.ReadFile(filepath.Join("testdata", "2023.3", name))
	require.NoError(s.t, err)
	_, err = w.Write(fixture)
	require.NoError(s.t, err)
}

func newClient(host string) *sspanel.APIClient {
	return sspanel.New(&api.Config{
		APIHost:  host,
		Key:      fixtureKey,
		NodeID:   fixtureNodeID,
		NodeType: "V2ray",
		Timeout:  1,
	})
}

func TestSSPanel2023Contract(t *testing.T) {
	recorder, server := newContractServer(t)
	client := newClient(server.URL)

	node, err := client.GetNodeInfo()
	require.NoError(t, err)
	require.Equal(t, uint32(12080), node.Port)
	require.Equal(t, "tcp", node.TransportProtocol)
	require.False(t, node.EnableVless)
	_, err = client.GetNodeInfo()
	require.EqualError(t, err, api.NodeNotModified)

	users, err := client.GetUserList()
	require.NoError(t, err)
	require.Equal(t, []api.UserInfo{{
		UID:         101,
		UUID:        "00000000-0000-4000-8000-000000000101",
		Passwd:      "fixture-password",
		Port:        12080,
		Method:      "aes-128-gcm",
		SpeedLimit:  2_000_000,
		DeviceLimit: 2,
	}}, *users)
	_, err = client.GetUserList()
	require.EqualError(t, err, api.UserNotModified)

	rules, err := client.GetNodeRule()
	require.NoError(t, err)
	require.Len(t, *rules, 1)
	require.Equal(t, 7, (*rules)[0].ID)
	require.True(t, (*rules)[0].Pattern.MatchString("blocked.invalid"))
	_, err = client.GetNodeRule()
	require.EqualError(t, err, api.RuleNotModified)

	requestCount := len(recorder.requests)
	require.NoError(t, client.ReportNodeStatus(&api.NodeStatus{CPU: 25, Mem: 50, Disk: 75, Uptime: 3600}))
	require.Len(t, recorder.requests, requestCount, "SSPanel 2023.3 has no node-status POST route")

	traffic := []api.UserTraffic{{UID: 101, Upload: 2048, Download: 4096}}
	require.NoError(t, client.ReportUserTraffic(&traffic))
	online := []api.OnlineUser{{UID: 101, IP: "203.0.113.10"}}
	require.NoError(t, client.ReportNodeOnlineUsers(&online))
	detections := []api.DetectResult{{UID: 101, RuleID: 7}}
	require.NoError(t, client.ReportIllegal(&detections))

	expected := []struct {
		method string
		path   string
		nodeID bool
		etag   string
		body   string
	}{
		{http.MethodGet, "/mod_mu/nodes/41/info", false, "", ""},
		{http.MethodGet, "/mod_mu/nodes/41/info", false, `"node-fixture-v1"`, ""},
		{http.MethodGet, "/mod_mu/users", true, "", ""},
		{http.MethodGet, "/mod_mu/users", true, `"users-fixture-v1"`, ""},
		{http.MethodGet, "/mod_mu/func/detect_rules", false, "", ""},
		{http.MethodGet, "/mod_mu/func/detect_rules", false, `"rules-fixture-v1"`, ""},
		{http.MethodPost, "/mod_mu/users/traffic", true, "", fixtureText(t, "traffic-report.json")},
		{http.MethodPost, "/mod_mu/users/aliveip", true, "", fixtureText(t, "alive-ip-report.json")},
		{http.MethodPost, "/mod_mu/users/detectlog", true, "", fixtureText(t, "detection-report.json")},
	}
	require.Len(t, recorder.requests, len(expected))
	for i, want := range expected {
		got := recorder.requests[i]
		require.Equal(t, want.method, got.method, "request %d method", i)
		require.Equal(t, want.path, got.path, "request %d path", i)
		require.Equal(t, fixtureKey, got.query.Get("key"), "request %d key", i)
		require.Equal(t, fixtureKey, got.query.Get("muKey"), "request %d muKey", i)
		if want.nodeID {
			require.Equal(t, "41", got.query.Get("node_id"), "request %d node_id", i)
		} else {
			require.NotContains(t, got.query, "node_id", "request %d node_id", i)
		}
		require.Equal(t, want.etag, got.etag, "request %d ETag", i)
		if want.body != "" {
			require.JSONEq(t, want.body, string(got.body), "request %d body", i)
		}
	}
}

func TestSSPanelRejectsUnsuccessfulResponseEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"ret":0,"data":[]}`))
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)

	_, err := newClient(server.URL).GetUserList()
	require.ErrorContains(t, err, "ret")
}

func TestFailedAliveIPReportKeepsLastSuccessfulReport(t *testing.T) {
	fail := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail {
			http.Error(w, "temporary failure", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"ret":1,"data":{}}`))
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	client := newClient(server.URL)

	first := []api.OnlineUser{{UID: 101, IP: "203.0.113.10"}}
	require.NoError(t, client.ReportNodeOnlineUsers(&first))
	require.Equal(t, map[int]int{101: 1}, client.LastReportOnline)

	fail = true
	second := []api.OnlineUser{{UID: 202, IP: "203.0.113.20"}}
	require.Error(t, client.ReportNodeOnlineUsers(&second))
	require.Equal(t, map[int]int{101: 1}, client.LastReportOnline)
}

func fixtureText(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "2023.3", name))
	require.NoError(t, err)
	return strings.TrimSpace(string(data))
}

func TestFixturesContainOnlySanitizedAddresses(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("testdata", "2023.3"))
	require.NoError(t, err)
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join("testdata", "2023.3", entry.Name()))
		require.NoError(t, err)
		require.NotContains(t, string(data), "example.com")
		require.NotContains(t, string(data), "127.0.0.1")
		var decoded any
		require.NoError(t, json.Unmarshal(data, &decoded), entry.Name())
	}
}
