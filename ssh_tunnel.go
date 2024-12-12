// Reference: https://gist.github.com/svett/5d695dcc4cc6ad5dd275

package tunnel

import (
	"fmt"
	logger "github.com/sirupsen/logrus"
	"golang.org/x/crypto/ssh"
	"io"
	"math/rand"
	"net"
	"strconv"
)

var (
	minLocalPort = 50000
	maxLocalPort = 65000
)

// SshTunnel Tunnel 接口的实现.
type SshTunnel struct {
	name                 string
	sshUsername          string
	sshPassword          string
	tunneledProtocol     string
	localTunnelEndpoint  string // 本地监听的ip和端口
	serverTunnelEndpoint string // 隧道监听的地址和端口
	remoteEndpoint       string // 最终的远端地址
	config               *ssh.ClientConfig
	localConns           []net.Conn    // 调用方和本地隧道监听端口之间已经建立的连接
	sshConns             []*ssh.Client // 本地隧道服务和真实的隧道（如ssh地址）已经建立的连接
	remoteConns          []net.Conn    // ssh服务端和真实的远端地址之间建立的连接
	willClose            bool          // 隧道当前状态是否要变为关闭状态，用于在异常发生时判断隧道是手动关闭还是发生异常了
	isClosed             bool          // 用于标记隧道是否关闭
}

func init() {
	CommunicationTunnelFactories["SSH"] = SshTunnelFactory
}

// SshTunnelFactory ssh隧道实现
func SshTunnelFactory(tunnelConfig *TunnelConfig) (Tunnel, error) {
	clientConfig := &ssh.ClientConfig{
		User: tunnelConfig.Username,
		Auth: []ssh.AuthMethod{
			ssh.Password(tunnelConfig.Password),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	sshServerAddr, sshPort, err := getSSHServerAddrAndPort(tunnelConfig)
	if err != nil {
		return nil, err
	}
	localPortNum := getRandomListeningPort()
	relativeRemoteAddr := getRelativeRemoteAddr(sshServerAddr, tunnelConfig.RemoteAddr)
	tunnel := &SshTunnel{
		name:                 tunnelConfig.Protocol,
		sshUsername:          tunnelConfig.Username,
		sshPassword:          tunnelConfig.Password,
		localTunnelEndpoint:  fmt.Sprintf("localhost:%d", localPortNum),
		serverTunnelEndpoint: fmt.Sprintf("%s:%d", sshServerAddr, sshPort),
		remoteEndpoint:       fmt.Sprintf("%s:%d", relativeRemoteAddr, tunnelConfig.RemotePort),
		config:               clientConfig,
		tunneledProtocol:     tunnelConfig.TunneledProtocol,
	}
	return tunnel, nil
}

// 获取远端地址，ssh服务地址可能和远端地址相同
func getRelativeRemoteAddr(sshServerAddr, remoteAddr string) string {
	if sshServerAddr == remoteAddr {
		return "localhost"
	}
	return remoteAddr
}

func getSSHServerAddrAndPort(tunnelConfig *TunnelConfig) (string, int, error) {
	sshEndpoint := tunnelConfig.TunnelEndpoint
	if portNum, err := strconv.Atoi(sshEndpoint); err == nil {
		return tunnelConfig.RemoteAddr, portNum, nil
	}
	return splitAddrAndPort(sshEndpoint, tunnelConfig.TunneledProtocol)
}

func (s *SshTunnel) GetName() string {
	return s.name
}

func (s *SshTunnel) GetLocalEndpoint() string {
	return fmt.Sprintf("%s://%s", s.tunneledProtocol, s.localTunnelEndpoint)
}

func (s *SshTunnel) GetRemoteEndpoint() string {
	return fmt.Sprintf("%s://%s", s.tunneledProtocol, s.remoteEndpoint)
}

// Start 必须以协程的方式运行
func (s *SshTunnel) Start(tunnelReady chan bool) {
	logger.Infof(fmt.Sprintf("Starting local tunnel endpoint at %s", s.localTunnelEndpoint))
	logger.Infof(fmt.Sprintf("Setting server tunnel endpoint at %s", s.serverTunnelEndpoint))
	logger.Infof(fmt.Sprintf("Setting remote endpoint at %s", s.remoteEndpoint))

	// 监听本地的隧道端点
	listener, err := net.Listen("tcp", s.localTunnelEndpoint)
	if err != nil {
		logger.Infof(fmt.Sprintf("[!] Error setting SSH tunnel listener: %s", err.Error()))
		tunnelReady <- false
		return
	}
	defer listener.Close()
	// 通知调用方，隧道已经准备好
	tunnelReady <- true
	for {
		// 监听本地连接，如果有新连接就负责转发
		logger.Infof("[*] Listening on local tunnel endpoint")
		localConn, err := listener.Accept()
		if err != nil {
			logger.Infof(fmt.Sprintf("[!] Error accepting local SSH tunnel connection: %s", err.Error()))
			continue
		}
		logger.Infof("[*] Accepted connection on local SSH tunnel endpoint")
		s.localConns = append(s.localConns, localConn)
		go s.forwardConnection(localConn)
	}
}

// 转发连接的数据
func (s *SshTunnel) forwardConnection(localConn net.Conn) {
	logger.Infof("[*] Forwarding connection to server")
	// 连接到ssh服务端
	logger.Infof("[*] try to connect to ssh server")
	serverConn, err := s.connectToServerSsh()
	if err != nil {
		logger.Infof(fmt.Sprintf("[!] Error connecting to server SSH endpoint: %s", err.Error()))
		localConn.Close()
		return
	}
	s.sshConns = append(s.sshConns, serverConn)

	// 基于ssh隧道直接向最终的服务地址建立连接
	logger.Infof("[*] try to connect to final endpoint by ssh tunnel")
	remoteConn, err := serverConn.Dial("tcp", s.remoteEndpoint)
	if err != nil {
		logger.Infof(fmt.Sprintf("[!] Error connecting to remote endpoint: %s", err.Error()))
		localConn.Close()
		serverConn.Close()
		return
	}
	s.remoteConns = append(s.remoteConns, remoteConn)

	logger.Infof("[*] Opened remote connection through tunnel, start forward traffic")
	forwarderFunc := func(writer, reader net.Conn) {
		defer writer.Close()
		defer reader.Close()

		if _, err = io.Copy(writer, reader); err != nil {
			if !s.willClose {
				// 如果不是调用方手动关闭的，需要显示具体的错误日志
				logger.Infof(fmt.Sprintf("[!] I/O copy error when forwarding through tunnel: %s", err.Error()))
			}
			localConn.Close()
			remoteConn.Close()
			serverConn.Close()
			s.isClosed = true
		}
	}
	// 转发本地连接和远程连接之间的流量
	go forwarderFunc(localConn, remoteConn)
	go forwarderFunc(remoteConn, localConn)
}

func (s *SshTunnel) connectToServerSsh() (*ssh.Client, error) {
	return ssh.Dial("tcp", s.serverTunnelEndpoint, s.config)
}

// 获取随机监听的端口
func getRandomListeningPort() int {
	return rand.Intn(maxLocalPort-minLocalPort) + minLocalPort
}

// Stop 停止隧道
func (s *SshTunnel) Stop() {
	logger.Infof("close conns established by tunnl")
	s.willClose = true
	for _, conn := range s.localConns {
		conn.Close()
	}
	for _, client := range s.sshConns {
		client.Close()
	}
	for _, conn := range s.remoteConns {
		conn.Close()
	}
	s.isClosed = true
}
