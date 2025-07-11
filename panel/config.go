package panel

import (
	"github.com/ariadarkkkis/XrayR/api"
	"github.com/ariadarkkkis/XrayR/service/controller"
)

type Config struct {
	LogConfig          *LogConfig        `mapstructure:"Log"`
	DnsConfigPath      string            `mapstructure:"DnsConfigPath"`
	InboundConfigPath  string            `mapstructure:"InboundConfigPath"`
	OutboundConfigPath string            `mapstructure:"OutboundConfigPath"`
	RouteConfigPath    string            `mapstructure:"RouteConfigPath"`
	ReverseConfigPath  string            `mapstructure:"ReverseConfigPath"`
	ConnectionConfig   *ConnectionConfig `mapstructure:"ConnectionConfig"`
	NodesConfig        []*NodesConfig    `mapstructure:"Nodes"`
}

type NodesConfig struct {
	PanelType        string             `mapstructure:"PanelType"`
	ApiConfig        *api.Config        `mapstructure:"ApiConfig"`
	ControllerConfig *controller.Config `mapstructure:"ControllerConfig"`
}

type LogConfig struct {
	Level      string `mapstructure:"Level"`
	AccessPath string `mapstructure:"AccessPath"`
	ErrorPath  string `mapstructure:"ErrorPath"`
}

type ConnectionConfig struct {
	Handshake    uint32 `mapstructure:"handshake"`
	ConnIdle     uint32 `mapstructure:"connIdle"`
	UplinkOnly   uint32 `mapstructure:"uplinkOnly"`
	DownlinkOnly uint32 `mapstructure:"downlinkOnly"`
	BufferSize   int32  `mapstructure:"bufferSize"`
}

type ReverseConfig struct {
	Bridges []*BridgeConfig `mapstructure:"bridges"`
	Portals []*PortalConfig `mapstructure:"portals"`
}

type BridgeConfig struct {
	Tag    string `mapstructure:"tag"`
	Domain string `mapstructure:"domain"`
}

type PortalConfig struct {
	Tag    string `mapstructure:"tag"`
	Domain string `mapstructure:"domain"`
}
