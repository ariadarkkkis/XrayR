package panel_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/xtls/xray-core/infra/conf"

	_ "github.com/ariadarkkkis/XrayR/cmd/distro/all"
	"github.com/ariadarkkkis/XrayR/panel"
)

func TestShippedCoreConfigurationsBuild(t *testing.T) {
	configDirectory, err := filepath.Abs(filepath.Join("..", "release", "config"))
	require.NoError(t, err)
	t.Setenv("xray.location.asset", configDirectory)

	t.Run("DNS", func(t *testing.T) {
		var config conf.DNSConfig
		readConfig(t, configDirectory, "dns.json", &config)
		_, err := config.Build()
		require.NoError(t, err)
	})
	t.Run("routing", func(t *testing.T) {
		var config conf.RouterConfig
		readConfig(t, configDirectory, "route.json", &config)
		_, err := config.Build()
		require.NoError(t, err)
	})
	t.Run("reverse", func(t *testing.T) {
		var config conf.ReverseConfig
		readConfig(t, configDirectory, "reverse.json", &config)
		_, err := config.Build()
		require.NoError(t, err)
	})
	t.Run("custom inbounds", func(t *testing.T) {
		var configs []conf.InboundDetourConfig
		readConfig(t, configDirectory, "custom_inbound.json", &configs)
		for index := range configs {
			_, err := configs[index].Build()
			require.NoErrorf(t, err, "custom inbound %d", index)
		}
	})
	t.Run("custom outbounds", func(t *testing.T) {
		var configs []conf.OutboundDetourConfig
		readConfig(t, configDirectory, "custom_outbound.json", &configs)
		for index := range configs {
			_, err := configs[index].Build()
			require.NoErrorf(t, err, "custom outbound %d", index)
		}
	})
}

func TestShippedCoreConfigurationsStartTogether(t *testing.T) {
	configDirectory, err := filepath.Abs(filepath.Join("..", "release", "config"))
	require.NoError(t, err)
	t.Setenv("xray.location.asset", configDirectory)

	runtime := panel.New(&panel.Config{
		DnsConfigPath:      filepath.Join(configDirectory, "dns.json"),
		RouteConfigPath:    filepath.Join(configDirectory, "route.json"),
		ReverseConfigPath:  filepath.Join(configDirectory, "reverse.json"),
		InboundConfigPath:  filepath.Join(configDirectory, "custom_inbound.json"),
		OutboundConfigPath: filepath.Join(configDirectory, "custom_outbound.json"),
	})
	runtime.Start()
	require.True(t, runtime.Running)
	t.Cleanup(runtime.Close)
}

func TestInvalidCustomOutboundCardinalityIsActionableAndSecretSafe(t *testing.T) {
	const secret = "fixture-secret-that-must-not-leak"
	data := []byte(`{
		"protocol":"shadowsocks",
		"settings":{"servers":[
			{"address":"127.0.0.1","port":1001,"method":"aes-128-gcm","password":"` + secret + `"},
			{"address":"127.0.0.1","port":1002,"method":"aes-128-gcm","password":"another-secret"}
		]}
	}`)
	var config conf.OutboundDetourConfig
	require.NoError(t, json.Unmarshal(data, &config))
	_, err := config.Build()
	require.ErrorContains(t, err, "one and only one")
	require.NotContains(t, err.Error(), secret)
}

func TestRemovedTLSVerificationBypassFailsWithMigrationGuidance(t *testing.T) {
	data := []byte(`{
		"protocol":"freedom",
		"settings":{},
		"streamSettings":{"security":"tls","tlsSettings":{"allowInsecure":true}}
	}`)
	var config conf.OutboundDetourConfig
	require.NoError(t, json.Unmarshal(data, &config))
	_, err := config.Build()
	require.ErrorContains(t, err, "allowInsecure")
	require.ErrorContains(t, err, "pinnedPeerCertSha256")
}

func readConfig(t *testing.T, directory, name string, target any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(directory, name))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, target))
}
