package mydispatcher_test

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appProxyman "github.com/xtls/xray-core/app/proxyman"
	_ "github.com/xtls/xray-core/app/proxyman/inbound"
	_ "github.com/xtls/xray-core/app/proxyman/outbound"
	appStats "github.com/xtls/xray-core/app/stats"
	xrayNet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
	featureRouting "github.com/xtls/xray-core/features/routing"
	featureStats "github.com/xtls/xray-core/features/stats"
	"github.com/xtls/xray-core/infra/conf"
	_ "github.com/xtls/xray-core/proxy/freedom"
	_ "github.com/xtls/xray-core/proxy/socks"

	"github.com/ariadarkkkis/XrayR/api"
	"github.com/ariadarkkkis/XrayR/app/mydispatcher"
)

func TestRealConnectionTraversesPolicyAndAccountingHooks(t *testing.T) {
	echoAddress := startEchoServer(t)
	proxyPort := availableTCPPort(t)
	server := startDispatcherCore(t, proxyPort)

	dispatcher := server.GetFeature(featureRouting.DispatcherType()).(*mydispatcher.DefaultDispatcher)
	users := []api.UserInfo{{UID: 7, Email: "fixture", SpeedLimit: 1000, DeviceLimit: 1}}
	require.NoError(t, dispatcher.Limiter.AddInboundLimiter("dispatcher", 0, &users, nil))

	conn := dialSOCKS5(t, proxyPort, "dispatcher|fixture|7", "fixture-password", echoAddress)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	payload := bytes.Repeat([]byte("x"), 1100)
	require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
	started := time.Now()
	_, err := conn.Write(payload)
	require.NoError(t, err)
	reply := make([]byte, len(payload))
	_, err = io.ReadFull(conn, reply)
	require.NoError(t, err)
	require.Equal(t, payload, reply)
	require.GreaterOrEqual(t, time.Since(started), 900*time.Millisecond, "configured user speed limit must throttle the real connection")

	manager := server.GetFeature(featureStats.ManagerType()).(featureStats.Manager)
	require.Eventually(t, func() bool {
		up := manager.GetCounter("user>>>dispatcher|fixture|7>>>traffic>>>uplink")
		down := manager.GetCounter("user>>>dispatcher|fixture|7>>>traffic>>>downlink")
		return up != nil && down != nil && up.Value() >= int64(len(payload)) && down.Value() >= int64(len(payload))
	}, 5*time.Second, 10*time.Millisecond)

	online, err := dispatcher.Limiter.GetOnlineDevice("dispatcher")
	require.NoError(t, err)
	require.Contains(t, *online, api.OnlineUser{UID: 7, IP: "127.0.0.1"})

	require.NoError(t, dispatcher.RuleManager.UpdateRule("dispatcher", []api.DetectRule{{
		ID:      99,
		Pattern: regexp.MustCompile(regexp.QuoteMeta(echoAddress.String())),
	}}))
	rejectedConn := dialSOCKS5(t, proxyPort, "dispatcher|fixture|7", "fixture-password", echoAddress)
	t.Cleanup(func() { _ = rejectedConn.Close() })
	require.NoError(t, rejectedConn.SetDeadline(time.Now().Add(5*time.Second)))
	_, _ = rejectedConn.Write([]byte("must be rejected"))
	_, err = rejectedConn.Read(make([]byte, 1))
	require.Error(t, err, "audit rule must terminate the real connection")
	detections, err := dispatcher.RuleManager.GetDetectResult("dispatcher")
	require.NoError(t, err)
	require.Contains(t, *detections, api.DetectResult{UID: 7, RuleID: 99})
}

func startDispatcherCore(t *testing.T, port int) *core.Instance {
	t.Helper()
	socksSettings, err := json.Marshal(&conf.SocksServerConfig{
		AuthMethod: conf.AuthMethodUserPass,
		Accounts: []*conf.SocksAccount{{
			Username: "dispatcher|fixture|7",
			Password: "fixture-password",
		}},
	})
	require.NoError(t, err)
	socksSettingsJSON := json.RawMessage(socksSettings)
	inboundConfig, err := (&conf.InboundDetourConfig{
		Protocol: "socks",
		Tag:      "dispatcher",
		ListenOn: &conf.Address{Address: xrayNet.ParseAddress("127.0.0.1")},
		PortList: &conf.PortList{Range: []conf.PortRange{{From: uint32(port), To: uint32(port)}}},
		Settings: &socksSettingsJSON,
	}).Build()
	require.NoError(t, err)
	freedomSettings := json.RawMessage(`{}`)
	outboundConfig, err := (&conf.OutboundDetourConfig{
		Protocol: "freedom",
		Tag:      "dispatcher",
		Settings: &freedomSettings,
	}).Build()
	require.NoError(t, err)
	policyConfig, err := (&conf.PolicyConfig{Levels: map[uint32]*conf.Policy{0: {
		StatsUserUplink:   true,
		StatsUserDownlink: true,
	}}}).Build()
	require.NoError(t, err)
	routerConfig, err := (&conf.RouterConfig{}).Build()
	require.NoError(t, err)

	server, err := core.New(&core.Config{
		App: []*serial.TypedMessage{
			serial.ToTypedMessage(&mydispatcher.Config{}),
			serial.ToTypedMessage(&appStats.Config{}),
			serial.ToTypedMessage(&appProxyman.InboundConfig{}),
			serial.ToTypedMessage(&appProxyman.OutboundConfig{}),
			serial.ToTypedMessage(policyConfig),
			serial.ToTypedMessage(routerConfig),
		},
		Inbound:  []*core.InboundHandlerConfig{inboundConfig},
		Outbound: []*core.OutboundHandlerConfig{outboundConfig},
	})
	require.NoError(t, err)
	require.NoError(t, server.Start())
	t.Cleanup(func() { require.NoError(t, server.Close()) })
	return server
}

func startEchoServer(t *testing.T) *net.TCPAddr {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, listener.Close()) })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return listener.Addr().(*net.TCPAddr)
}

func availableTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	return port
}

func dialSOCKS5(t *testing.T, proxyPort int, username, password string, destination *net.TCPAddr) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", binaryPort(proxyPort)), 5*time.Second)
	require.NoError(t, err)
	require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
	_, err = conn.Write([]byte{5, 1, 2})
	require.NoError(t, err)
	response := make([]byte, 2)
	_, err = io.ReadFull(conn, response)
	require.NoError(t, err)
	require.Equal(t, []byte{5, 2}, response)
	auth := append([]byte{1, byte(len(username))}, []byte(username)...)
	auth = append(auth, byte(len(password)))
	auth = append(auth, []byte(password)...)
	_, err = conn.Write(auth)
	require.NoError(t, err)
	_, err = io.ReadFull(conn, response)
	require.NoError(t, err)
	require.Equal(t, []byte{1, 0}, response)

	request := []byte{5, 1, 0, 1}
	request = append(request, destination.IP.To4()...)
	port := make([]byte, 2)
	binary.BigEndian.PutUint16(port, uint16(destination.Port))
	request = append(request, port...)
	_, err = conn.Write(request)
	require.NoError(t, err)
	reply := make([]byte, 10)
	_, err = io.ReadFull(conn, reply)
	require.NoError(t, err)
	require.Equal(t, byte(0), reply[1])
	require.NoError(t, conn.SetDeadline(time.Time{}))
	return conn
}

func binaryPort(port int) string {
	return fmt.Sprintf("%d", port)
}
