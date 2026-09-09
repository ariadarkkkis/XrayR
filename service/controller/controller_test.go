package controller_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	appProxyman "github.com/xtls/xray-core/app/proxyman"
	appStats "github.com/xtls/xray-core/app/stats"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/inbound"
	"github.com/xtls/xray-core/features/routing"
	featureStats "github.com/xtls/xray-core/features/stats"
	"github.com/xtls/xray-core/infra/conf"

	"github.com/ariadarkkkis/XrayR/api"
	"github.com/ariadarkkkis/XrayR/api/sspanel"
	"github.com/ariadarkkkis/XrayR/app/mydispatcher"
	_ "github.com/ariadarkkkis/XrayR/cmd/distro/all"
	"github.com/ariadarkkkis/XrayR/service/controller"
)

type cycleRequest struct {
	method string
	path   string
	body   []byte
}

type fixturePanel struct {
	t        *testing.T
	port     int
	mu       sync.Mutex
	getCount map[string]int
	requests []cycleRequest
	failures panelFailureMode
}

type panelFailureMode struct {
	nodeRefresh     bool
	trafficReport   bool
	aliveIPReport   bool
	detectionReport bool
}

func newFixturePanel(t *testing.T, port int) (*fixturePanel, *httptest.Server) {
	t.Helper()
	panel := &fixturePanel{t: t, port: port, getCount: make(map[string]int)}
	server := httptest.NewServer(http.HandlerFunc(panel.serveHTTP))
	t.Cleanup(server.Close)
	return panel, server
}

func (p *fixturePanel) serveHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	require.NoError(p.t, err)

	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, cycleRequest{method: r.Method, path: r.URL.Path, body: body})
	require.Equal(p.t, "fixture-api-key", r.URL.Query().Get("key"))
	require.Equal(p.t, "fixture-api-key", r.URL.Query().Get("muKey"))

	if r.Method == http.MethodGet {
		p.getCount[r.URL.Path]++
		if r.URL.Path == "/mod_mu/nodes/41/info" && p.getCount[r.URL.Path] > 1 && p.failures.nodeRefresh {
			http.Error(w, "temporary refresh failure", http.StatusServiceUnavailable)
			return
		}
		if p.getCount[r.URL.Path] > 1 {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	if (r.URL.Path == "/mod_mu/users/traffic" && p.failures.trafficReport) ||
		(r.URL.Path == "/mod_mu/users/aliveip" && p.failures.aliveIPReport) ||
		(r.URL.Path == "/mod_mu/users/detectlog" && p.failures.detectionReport) {
		http.Error(w, "temporary report failure", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/mod_mu/nodes/41/info":
		w.Header().Set("ETag", `"node-fixture-v1"`)
		_, err = w.Write(p.nodeFixture())
	case "/mod_mu/users":
		w.Header().Set("ETag", `"users-fixture-v1"`)
		_, err = w.Write(p.usersFixture())
	case "/mod_mu/func/detect_rules":
		w.Header().Set("ETag", `"rules-fixture-v1"`)
		_, err = w.Write(readFixture(p.t, "detection-rules.json"))
	case "/mod_mu/users/traffic", "/mod_mu/users/aliveip", "/mod_mu/users/detectlog":
		_, err = w.Write([]byte(`{"ret":1,"data":{}}`))
	default:
		http.Error(w, "unexpected route", http.StatusNotFound)
	}
	require.NoError(p.t, err)
}

func (p *fixturePanel) nodeFixture() []byte {
	var envelope map[string]any
	require.NoError(p.t, json.Unmarshal(readFixture(p.t, "node.json"), &envelope))
	data := envelope["data"].(map[string]any)
	data["server"] = fmt.Sprintf("fixture.invalid;%d;0;;tcp;path=/fixture|host=fixture.invalid", p.port)
	data["custom_config"].(map[string]any)["offset_port_node"] = strconv.Itoa(p.port)
	result, err := json.Marshal(envelope)
	require.NoError(p.t, err)
	return result
}

func (p *fixturePanel) usersFixture() []byte {
	var envelope map[string]any
	require.NoError(p.t, json.Unmarshal(readFixture(p.t, "users.json"), &envelope))
	envelope["data"].([]any)[0].(map[string]any)["port"] = p.port
	result, err := json.Marshal(envelope)
	require.NoError(p.t, err)
	return result
}

func (p *fixturePanel) setFailures(failures panelFailureMode) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failures = failures
}

func (p *fixturePanel) snapshot() []cycleRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]cycleRequest(nil), p.requests...)
}

func TestSSPanel2023ControllerReconciliationAndReportingCycle(t *testing.T) {
	port := availablePort(t)
	panel, panelServer := newFixturePanel(t, port)
	server := startCore(t)
	client := newSSPanelClient(panelServer.URL)
	c := controller.New(server, client, testControllerConfig(), "SSpanel")
	require.NoError(t, c.StartWithoutScheduling())
	t.Cleanup(func() { require.NoError(t, c.Close()) })

	userTag, upCounter, downCounter := seedRuntimeActivity(t, server, c.Tag)
	require.NotEmpty(t, userTag)

	require.NoError(t, c.ReconcileAndReportOnce())
	require.Zero(t, upCounter.Value())
	require.Zero(t, downCounter.Value())

	requests := panel.snapshot()
	assertReport(t, requests, "/mod_mu/users/traffic", "traffic-report.json")
	assertReport(t, requests, "/mod_mu/users/aliveip", "alive-ip-report.json")
	assertReport(t, requests, "/mod_mu/users/detectlog", "detection-report.json")
	for _, request := range requests {
		require.False(t, request.method == http.MethodPost && request.path == "/mod_mu/nodes/41/info",
			"SSPanel 2023.3 must not receive node-status reports")
	}
}

func TestCycleKeepsRuntimeAndTrafficAcrossPanelFailures(t *testing.T) {
	port := availablePort(t)
	panel, panelServer := newFixturePanel(t, port)
	server := startCore(t)
	c := controller.New(server, newSSPanelClient(panelServer.URL), testControllerConfig(), "SSpanel")
	require.NoError(t, c.StartWithoutScheduling())
	t.Cleanup(func() { require.NoError(t, c.Close()) })

	_, upCounter, downCounter := seedRuntimeActivity(t, server, c.Tag)
	panel.setFailures(panelFailureMode{
		nodeRefresh:     true,
		trafficReport:   true,
		aliveIPReport:   true,
		detectionReport: true,
	})
	err := c.ReconcileAndReportOnce()
	require.ErrorContains(t, err, "refresh")
	require.ErrorContains(t, err, "traffic")
	require.ErrorContains(t, err, "online")
	require.ErrorContains(t, err, "detection")
	require.EqualValues(t, 2048, upCounter.Value())
	require.EqualValues(t, 4096, downCounter.Value())

	manager := server.GetFeature(inbound.ManagerType()).(inbound.Manager)
	_, err = manager.GetHandler(context.Background(), c.Tag)
	require.NoError(t, err, "a failed refresh must leave the last usable inbound running")

	panel.setFailures(panelFailureMode{})
	require.NoError(t, c.ReconcileAndReportOnce())
	require.Zero(t, upCounter.Value())
	require.Zero(t, downCounter.Value())

	requests := panel.snapshot()
	assertReports(t, requests, "/mod_mu/users/traffic", "traffic-report.json", 2)
	assertReports(t, requests, "/mod_mu/users/aliveip", "alive-ip-report.json", 2)
	assertReports(t, requests, "/mod_mu/users/detectlog", "detection-report.json", 2)
}

func newSSPanelClient(host string) *sspanel.APIClient {
	return sspanel.New(&api.Config{
		APIHost:  host,
		Key:      "fixture-api-key",
		NodeID:   41,
		NodeType: "V2ray",
		Timeout:  1,
	})
}

func testControllerConfig() *controller.Config {
	return &controller.Config{
		ListenIP:               "127.0.0.1",
		SendIP:                 "0.0.0.0",
		UpdatePeriodic:         60,
		DeviceOnlineMinTraffic: 0,
	}
}

func startCore(t *testing.T) *core.Instance {
	t.Helper()
	policy, err := (&conf.PolicyConfig{Levels: map[uint32]*conf.Policy{0: {
		StatsUserUplink:   true,
		StatsUserDownlink: true,
	}}}).Build()
	require.NoError(t, err)
	router, err := (&conf.RouterConfig{}).Build()
	require.NoError(t, err)

	server, err := core.New(&core.Config{App: []*serial.TypedMessage{
		serial.ToTypedMessage(&mydispatcher.Config{}),
		serial.ToTypedMessage(&appStats.Config{}),
		serial.ToTypedMessage(&appProxyman.InboundConfig{}),
		serial.ToTypedMessage(&appProxyman.OutboundConfig{}),
		serial.ToTypedMessage(policy),
		serial.ToTypedMessage(router),
	}})
	require.NoError(t, err)
	require.NoError(t, server.Start())
	t.Cleanup(func() { require.NoError(t, server.Close()) })
	return server
}

func seedRuntimeActivity(t *testing.T, server *core.Instance, tag string) (string, featureStats.Counter, featureStats.Counter) {
	t.Helper()
	userTag, upCounter, downCounter := seedTraffic(t, server, tag)
	dispatcher := server.GetFeature(routing.DispatcherType()).(*mydispatcher.DefaultDispatcher)
	_, _, rejected := dispatcher.Limiter.GetUserBucket(tag, userTag, "203.0.113.10", true)
	require.False(t, rejected)
	require.True(t, dispatcher.RuleManager.Detect(tag, "blocked.invalid", userTag))
	return userTag, upCounter, downCounter
}

func seedTraffic(t *testing.T, server *core.Instance, tag string) (string, featureStats.Counter, featureStats.Counter) {
	t.Helper()
	userTag := tag + "||101"
	manager := server.GetFeature(featureStats.ManagerType()).(featureStats.Manager)
	upCounter, err := featureStats.GetOrRegisterCounter(manager, "user>>>"+userTag+">>>traffic>>>uplink")
	require.NoError(t, err)
	downCounter, err := featureStats.GetOrRegisterCounter(manager, "user>>>"+userTag+">>>traffic>>>downlink")
	require.NoError(t, err)
	upCounter.Add(2048)
	downCounter.Add(4096)
	return userTag, upCounter, downCounter
}

func assertReport(t *testing.T, requests []cycleRequest, path, fixture string) {
	t.Helper()
	assertReports(t, requests, path, fixture, 1)
}

func assertReports(t *testing.T, requests []cycleRequest, path, fixture string, count int) {
	t.Helper()
	var matched int
	for _, request := range requests {
		if request.method == http.MethodPost && request.path == path {
			require.JSONEq(t, string(readFixture(t, fixture)), string(request.body))
			matched++
		}
	}
	require.Equal(t, count, matched, "POST %s count", path)
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "api", "sspanel", "testdata", "2023.3", name))
	require.NoError(t, err)
	return data
}

func availablePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	return port
}
