package tunnel

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Tunnel 隧道接口
type Tunnel interface {
	GetName() string
	Start(tunnelReady chan bool) // 必须以协程异步运行
	Stop()                       // 关闭隧道，以释放连接资源
	GetLocalEndpoint() string    // 获取本地监听的端点
	GetRemoteEndpoint() string   // 获取远程的端点
}

type TunnelConfig struct {
	Protocol         string // 隧道协议，如通过ssh隧道封装http流量
	TunnelEndpoint   string // 隧道的地址，如ssh的ip
	Username         string // 隧道认证的账号
	Password         string // 隧道认证的密码
	RemoteAddr       string // 透过隧道后最终要连接的地址
	RemotePort       int    // 透过隧道后最终要连接的端口
	TunneledProtocol string // 被隧道封装的协议，如http
}

// CommunicationTunnelFactories 隧道工厂
var CommunicationTunnelFactories = map[string]func(tunnelConfig *TunnelConfig) (Tunnel, error){}
var defaultProtocolPorts = map[string]string{
	"http":  "80",
	"https": "443",
}

func GetAvailableCommTunnels() []string {
	tunnelNames := make([]string, 0, len(CommunicationTunnelFactories))
	for name := range CommunicationTunnelFactories {
		tunnelNames = append(tunnelNames, name)
	}
	return tunnelNames
}

// BuildTunnelConfig 构建隧道配置
func BuildTunnelConfig(protocol, tunnelEndpoint, destEndpoint, user, password string) (*TunnelConfig, error) {
	tunneledProtocol, remoteEndpoint := getTunneledProtocolAndRemoteAddr(destEndpoint)
	remoteAddr, remotePort, err := splitAddrAndPort(remoteEndpoint, tunneledProtocol)
	if err != nil {
		return nil, err
	}
	return &TunnelConfig{
		Protocol:         protocol, // 隧道协议、端点及账号密码
		TunnelEndpoint:   tunnelEndpoint,
		Username:         user,
		Password:         password,
		RemoteAddr:       remoteAddr, // 真实的远程地址和远程端口
		RemotePort:       remotePort,
		TunneledProtocol: tunneledProtocol, // 被隧道包裹的协议，也就是原始协议
	}, nil
}

// 获取被隧道的协议和地址
//
// Examples:
//
//	https://10.10.10.10:8888 -> https, 10.10.10.10:8888
//	10.10.10.10.:8888 -> http, 10.10.10.10:8888
func getTunneledProtocolAndRemoteAddr(remoteAddr string) (string, string) {
	protocolSplit := strings.Split(remoteAddr, "://")
	if len(protocolSplit) == 1 {
		// No protocol was specified.
		return "http", protocolSplit[0]
	} else {
		return protocolSplit[0], protocolSplit[1]
	}
}

// splitAddrAndPort 分割出ip和端口
func splitAddrAndPort(addrAndPort string, protocol string) (string, int, error) {
	addrPortSplit := strings.Split(addrAndPort, ":")
	addr := addrPortSplit[0]
	var portStr string
	if len(addrPortSplit) == 1 {
		// 没有提供端口的话，就使用默认的端口
		if defaultPort, ok := defaultProtocolPorts[protocol]; ok {
			portStr = defaultPort
		} else {
			return "", -1, errors.New(fmt.Sprintf("could not get default port for protocol %s", protocol))
		}
	} else {
		portStr = addrPortSplit[1]
	}
	if len(addr) == 0 {
		return "", -1, errors.New("empty address/hostname provided.")
	}
	if len(portStr) == 0 {
		return "", -1, errors.New("empty port provided.")
	}

	portNum, err := strconv.Atoi(portStr)
	if err != nil {
		return "", -1, errors.New(fmt.Sprintf("invalid endpoint provided: %s", addrAndPort))
	}
	return addr, portNum, nil
}

// 解析端点地址 (e.g. http://192.168.10.1:8888) 为协议、ip、端口字符窜
// 仅支持ipv4
func getEndpointInfo(endpointAddr string) (string, string, string, error) {
	protocolSplit := strings.Split(endpointAddr, "://")
	var addrAndPort string
	protocol := ""
	if len(protocolSplit) == 1 {
		// 未指定协议
		addrAndPort = protocolSplit[0]
	} else {
		addrAndPort = protocolSplit[1]
		protocol = protocolSplit[0]
	}
	addrPortSplit := strings.Split(addrAndPort, ":")
	addr := addrPortSplit[0]
	var port string
	if len(addrPortSplit) == 1 {
		// 未指定端口，那么使用默认的端口
		if defaultPort, ok := defaultProtocolPorts[protocol]; ok {
			port = defaultPort
		} else {
			return "", "", "", errors.New(fmt.Sprintf("could not get default port for protocol %s", protocol))
		}
	} else {
		port = addrPortSplit[1]
	}
	if len(addr) == 0 {
		return "", "", "", errors.New("empty address/hostname provided.")
	}
	if len(port) == 0 {
		return "", "", "", errors.New("empty port provided.")
	}
	return protocol, addr, port, nil
}

// FastStartTunnel 快速启动一个隧道，不使用时需要调用Stop进行关闭，以释放连接
func FastStartTunnel(tunnelConfig TunnelConfig) (Tunnel, error) {
	tunnelFactoryFunc, ok := CommunicationTunnelFactories[tunnelConfig.Protocol]
	if !ok {
		return nil, fmt.Errorf("not supported tunnel protocol: %s", tunnelConfig.Protocol)
	}

	// 实例化一个tunnel
	tunnelInstance, err := tunnelFactoryFunc(&tunnelConfig)
	if err != nil {
		return nil, fmt.Errorf("create tunnel instance failed, err: %w", err)
	}
	tunnelReady := make(chan bool)

	// 异步启动隧道
	go tunnelInstance.Start(tunnelReady)

	// 等待隧道准备好后向tunnelReady channel发送信号
	<-tunnelReady
	return tunnelInstance, nil
}
