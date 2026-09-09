package controller_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/inbound"
	"github.com/xtls/xray-core/proxy"

	"github.com/ariadarkkkis/XrayR/api"
	"github.com/ariadarkkkis/XrayR/service/controller"
)

func TestDynamicUsersAndHandlersReconcileWithoutLosingLastWorkingRuntime(t *testing.T) {
	firstPort := availablePort(t)
	client := &mutableAPI{
		node:  api.NodeInfo{NodeType: "Vmess", NodeID: 51, Port: uint32(firstPort), TransportProtocol: "tcp"},
		users: []api.UserInfo{{UID: 1, Email: "first", UUID: "00000000-0000-4000-8000-000000000001"}},
	}
	server := startCore(t)
	c := controller.New(server, client, &controller.Config{ListenIP: "127.0.0.1", SendIP: "0.0.0.0", UpdatePeriodic: 60, DisableGetRule: true, DisableLocalREALITYConfig: true}, "fixture")
	require.NoError(t, c.StartWithoutScheduling())
	t.Cleanup(func() { require.NoError(t, c.Close()) })

	client.users = []api.UserInfo{
		{UID: 1, Email: "first", UUID: "00000000-0000-4000-8000-000000000011"},
		{UID: 2, Email: "second", UUID: "00000000-0000-4000-8000-000000000002"},
	}
	require.NoError(t, c.ReconcileAndReportOnce())
	reader := runtimeUsers(t, server, c.Tag)
	require.NotNil(t, reader.GetUser(t.Context(), c.Tag+"|first|1"))
	require.NotNil(t, reader.GetUser(t.Context(), c.Tag+"|second|2"))

	client.users = nil
	require.NoError(t, c.ReconcileAndReportOnce())
	reader = runtimeUsers(t, server, c.Tag)
	require.Nil(t, reader.GetUser(t.Context(), c.Tag+"|first|1"))
	require.Nil(t, reader.GetUser(t.Context(), c.Tag+"|second|2"))

	oldTag := c.Tag
	client.node.Port = uint32(availablePort(t))
	client.users = []api.UserInfo{{UID: 3, Email: "third", UUID: "00000000-0000-4000-8000-000000000003"}}
	require.NoError(t, c.ReconcileAndReportOnce())
	manager := server.GetFeature(inbound.ManagerType()).(inbound.Manager)
	_, err := manager.GetHandler(context.Background(), oldTag)
	require.Error(t, err)
	_, err = manager.GetHandler(context.Background(), c.Tag)
	require.NoError(t, err)

	lastWorkingTag := c.Tag
	client.node = api.NodeInfo{
		NodeType:          "Vless",
		NodeID:            51,
		Port:              uint32(availablePort(t)),
		TransportProtocol: "tcp",
		EnableREALITY:     true,
		REALITYConfig: &api.REALITYConfig{
			Dest:        "fixture.invalid:443",
			ServerNames: []string{"fixture.invalid"},
			PrivateKey:  "not-reached",
			ShortIds:    []string{"invalid-short-id"},
		},
	}
	err = c.ReconcileAndReportOnce()
	require.ErrorContains(t, err, "REALITY shortIds")
	require.Equal(t, lastWorkingTag, c.Tag)
	_, err = manager.GetHandler(context.Background(), lastWorkingTag)
	require.NoError(t, err, "invalid refresh must leave the last working handler running")
}

type runtimeUserReader interface {
	GetUser(context.Context, string) *protocol.MemoryUser
}

func runtimeUsers(t *testing.T, server *core.Instance, tag string) runtimeUserReader {
	t.Helper()
	manager := server.GetFeature(inbound.ManagerType()).(inbound.Manager)
	handler, err := manager.GetHandler(context.Background(), tag)
	require.NoError(t, err)
	provider, ok := handler.(proxy.GetInbound)
	require.True(t, ok)
	reader, ok := provider.GetInbound().(runtimeUserReader)
	require.True(t, ok)
	return reader
}

type mutableAPI struct {
	node  api.NodeInfo
	users []api.UserInfo
}

func (m *mutableAPI) GetNodeInfo() (*api.NodeInfo, error) {
	copy := m.node
	return &copy, nil
}

func (m *mutableAPI) GetUserList() (*[]api.UserInfo, error) {
	copy := append([]api.UserInfo(nil), m.users...)
	return &copy, nil
}

func (m *mutableAPI) ReportNodeStatus(*api.NodeStatus) error        { return nil }
func (m *mutableAPI) ReportNodeOnlineUsers(*[]api.OnlineUser) error { return nil }
func (m *mutableAPI) ReportUserTraffic(*[]api.UserTraffic) error    { return nil }
func (m *mutableAPI) ReportIllegal(*[]api.DetectResult) error       { return nil }
func (m *mutableAPI) Describe() api.ClientInfo {
	return api.ClientInfo{APIHost: "fixture", NodeID: 51, NodeType: "Vmess"}
}
func (m *mutableAPI) GetNodeRule() (*[]api.DetectRule, error) {
	rules := []api.DetectRule{}
	return &rules, nil
}
func (m *mutableAPI) Debug() {}

var _ api.API = (*mutableAPI)(nil)
