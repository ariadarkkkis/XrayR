package controller_test

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ariadarkkkis/XrayR/api"
	"github.com/ariadarkkkis/XrayR/common/mylego"
	. "github.com/ariadarkkkis/XrayR/service/controller"
	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"github.com/xtls/xray-core/app/proxyman"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/inbound"
)

func TestBuildV2ray(t *testing.T) {
	nodeInfo := &api.NodeInfo{
		NodeType:          "V2ray",
		NodeID:            1,
		Port:              1145,
		SpeedLimit:        0,
		AlterID:           2,
		TransportProtocol: "ws",
		Host:              "test.test.tk",
		Path:              "v2ray",
		EnableTLS:         false,
	}
	certConfig := &mylego.CertConfig{
		CertMode:   "http",
		CertDomain: "test.test.tk",
		Provider:   "alidns",
		Email:      "test@gmail.com",
	}
	config := &Config{
		CertConfig: certConfig,
	}
	_, err := InboundBuilder(config, nodeInfo, "test_tag")
	if err != nil {
		t.Error(err)
	}
}

func TestBuildTrojan(t *testing.T) {
	nodeInfo := &api.NodeInfo{
		NodeType:          "Trojan",
		NodeID:            1,
		Port:              1145,
		SpeedLimit:        0,
		AlterID:           2,
		TransportProtocol: "tcp",
		Host:              "trojan.test.tk",
		Path:              "v2ray",
		EnableTLS:         false,
	}
	DNSEnv := make(map[string]string)
	DNSEnv["ALICLOUD_ACCESS_KEY"] = "aaa"
	DNSEnv["ALICLOUD_SECRET_KEY"] = "bbb"
	certConfig := &mylego.CertConfig{
		CertMode:   "dns",
		CertDomain: "trojan.test.tk",
		Provider:   "alidns",
		Email:      "test@gmail.com",
		DNSEnv:     DNSEnv,
	}
	config := &Config{
		CertConfig: certConfig,
	}
	_, err := InboundBuilder(config, nodeInfo, "test_tag")
	if err != nil {
		t.Error(err)
	}
}

func TestBuildSS(t *testing.T) {
	nodeInfo := &api.NodeInfo{
		NodeType:          "Shadowsocks",
		NodeID:            1,
		Port:              1145,
		SpeedLimit:        0,
		AlterID:           2,
		TransportProtocol: "tcp",
		CypherMethod:      "aes-128-gcm",
		Host:              "test.test.tk",
		Path:              "v2ray",
		EnableTLS:         false,
	}
	DNSEnv := make(map[string]string)
	DNSEnv["ALICLOUD_ACCESS_KEY"] = "aaa"
	DNSEnv["ALICLOUD_SECRET_KEY"] = "bbb"
	certConfig := &mylego.CertConfig{
		CertMode:   "dns",
		CertDomain: "trojan.test.tk",
		Provider:   "alidns",
		Email:      "test@me.com",
		DNSEnv:     DNSEnv,
	}
	config := &Config{
		CertConfig: certConfig,
	}
	_, err := InboundBuilder(config, nodeInfo, "test_tag")
	if err != nil {
		t.Error(err)
	}
}

func TestDisableIVCheckIsAcceptedWithDeprecationWarning(t *testing.T) {
	var logs bytes.Buffer
	logger := log.StandardLogger()
	previousOutput := logger.Out
	logger.SetOutput(&logs)
	t.Cleanup(func() { logger.SetOutput(previousOutput) })

	_, err := InboundBuilder(&Config{DisableIVCheck: true}, &api.NodeInfo{
		NodeType:          "Shadowsocks",
		NodeID:            23,
		Port:              1145,
		TransportProtocol: "tcp",
		CypherMethod:      "aes-128-gcm",
	}, "test_tag")
	require.NoError(t, err)
	require.Contains(t, logs.String(), "DisableIVCheck")
	require.Contains(t, strings.ToLower(logs.String()), "no longer supported")
}

func TestInvalidShadowsocksMethodHasActionableError(t *testing.T) {
	_, err := InboundBuilder(&Config{}, &api.NodeInfo{
		NodeType:          "Shadowsocks",
		NodeID:            24,
		Port:              1145,
		TransportProtocol: "tcp",
		CypherMethod:      "rc4-md5",
	}, "test_tag")
	require.ErrorContains(t, err, "node 24")
	require.ErrorContains(t, err, "unsupported Shadowsocks method")
}

func TestInvalidREALITYShortIDHasActionableSecretSafeError(t *testing.T) {
	const invalidShortID = "not-a-secret-short-id"
	_, err := InboundBuilder(&Config{DisableLocalREALITYConfig: true}, &api.NodeInfo{
		NodeType:          "Vless",
		NodeID:            25,
		Port:              1145,
		TransportProtocol: "tcp",
		EnableREALITY:     true,
		REALITYConfig: &api.REALITYConfig{
			Dest:        "example.com:443",
			ServerNames: []string{"example.com"},
			PrivateKey:  "unused-because-validation-runs-first",
			ShortIds:    []string{invalidShortID},
		},
	}, "test_tag")
	require.ErrorContains(t, err, "node 25 REALITY shortIds[0]")
	require.NotContains(t, err.Error(), invalidShortID)
}

func TestProtocolTransportCompatibilityMatrix(t *testing.T) {
	certConfig := testCertificate(t)
	realityKey := make([]byte, 32)
	_, err := rand.Read(realityKey)
	require.NoError(t, err)
	shadowsocks2022Key := make([]byte, 32)
	_, err = rand.Read(shadowsocks2022Key)
	require.NoError(t, err)

	tests := []struct {
		name        string
		node        api.NodeInfo
		config      Config
		assertBuilt func(*testing.T, *core.InboundHandlerConfig)
	}{
		{name: "VMess over TCP", node: api.NodeInfo{NodeType: "Vmess", TransportProtocol: "tcp"}},
		{name: "SSPanel VLESS over WebSocket", node: api.NodeInfo{NodeType: "V2ray", EnableVless: true, TransportProtocol: "ws", Host: "fixture.invalid", Path: "/ws"}},
		{
			name:   "Trojan over gRPC with socket settings",
			node:   api.NodeInfo{NodeType: "Trojan", TransportProtocol: "grpc", ServiceName: "fixture"},
			config: Config{EnableProxyProtocol: true},
			assertBuilt: func(t *testing.T, built *core.InboundHandlerConfig) {
				receiverMessage, err := built.ReceiverSettings.GetInstance()
				require.NoError(t, err)
				receiver := receiverMessage.(*proxyman.ReceiverConfig)
				require.NotNil(t, receiver.StreamSettings.SocketSettings)
				require.True(t, receiver.StreamSettings.SocketSettings.AcceptProxyProtocol)
			},
		},
		{name: "Shadowsocks over HTTPUpgrade", node: api.NodeInfo{NodeType: "Shadowsocks", TransportProtocol: "httpupgrade", CypherMethod: "aes-128-gcm", Path: "/upgrade"}},
		{name: "Shadowsocks 2022 over SplitHTTP", node: api.NodeInfo{NodeType: "Shadowsocks", TransportProtocol: "splithttp", CypherMethod: "2022-blake3-aes-256-gcm", ServerKey: base64.StdEncoding.EncodeToString(shadowsocks2022Key), Path: "/split"}},
		{name: "VLESS over XHTTP", node: api.NodeInfo{NodeType: "Vless", TransportProtocol: "xhttp", Path: "/xhttp"}},
		{name: "VLESS with TLS over WebSocket", node: api.NodeInfo{NodeType: "Vless", TransportProtocol: "ws", Path: "/tls", EnableTLS: true}, config: Config{CertConfig: certConfig, EnableProxyProtocol: true}},
		{name: "VLESS with REALITY", node: api.NodeInfo{NodeType: "Vless", TransportProtocol: "tcp", EnableREALITY: true, REALITYConfig: &api.REALITYConfig{Dest: "127.0.0.1:443", ServerNames: []string{"fixture.invalid"}, PrivateKey: base64.RawURLEncoding.EncodeToString(realityKey), ShortIds: []string{"0123456789abcdef"}}}, config: Config{DisableLocalREALITYConfig: true}},
		{name: "VLESS with fallback", node: api.NodeInfo{NodeType: "Vless", TransportProtocol: "tcp"}, config: Config{EnableFallback: true, FallBackConfigs: []*FallBackConfig{{SNI: "fixture.invalid", Dest: "127.0.0.1:9"}}}},
		{name: "Trojan with fallback", node: api.NodeInfo{NodeType: "Trojan", TransportProtocol: "tcp"}, config: Config{EnableFallback: true, FallBackConfigs: []*FallBackConfig{{SNI: "fixture.invalid", Dest: "127.0.0.1:9"}}}},
	}

	for index := range tests {
		test := tests[index]
		t.Run(test.name, func(t *testing.T) {
			test.node.NodeID = index + 100
			test.node.Port = uint32(availablePort(t))
			built, err := InboundBuilder(&test.config, &test.node, "matrix")
			require.NoError(t, err)
			if test.assertBuilt != nil {
				test.assertBuilt(t, built)
			}
			server := startCore(t)
			manager := server.GetFeature(inbound.ManagerType()).(inbound.Manager)
			require.NoError(t, core.AddInboundHandler(server, built))
			require.NoError(t, manager.RemoveHandler(t.Context(), "matrix"))
		})
	}
}

func testCertificate(t *testing.T) *mylego.CertConfig {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "fixture.invalid"},
		DNSNames:     []string{"fixture.invalid"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	certificate, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	require.NoError(t, err)
	directory := t.TempDir()
	certPath := filepath.Join(directory, "cert.pem")
	keyPath := filepath.Join(directory, "key.pem")
	require.NoError(t, os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate}), 0o600))
	require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)}), 0o600))
	return &mylego.CertConfig{CertMode: "file", CertFile: certPath, KeyFile: keyPath}
}
