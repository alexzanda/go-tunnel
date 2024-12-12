package main

import (
	"context"
	logger "github.com/sirupsen/logrus"
	"os/signal"
	"syscall"
	"tunnel"
)

// 创建一个ssh隧道，ssh隧道端点为10.50.122.50虚拟机，最终目标地址为里面的192.168.1.111，监听一个本地端口，如果有请求就通过隧道往最终的地址转发
// 示例，在下列样例中，执行go run example/example.go，会随机监听一个本地的端口比如，localhost:59205，访问这个地址的http请求，会先跟
// 192.170.2.50:22建立ssh隧道，然后由192.170.2.50转发到192.168.1.111的80这个端口上的http服务

func main() {
	tunnelConfig := tunnel.TunnelConfig{
		Protocol:         "SSH",
		TunnelEndpoint:   "10.50.122.50:22",
		Username:         "root",
		Password:         "dbapp#2023",
		RemoteAddr:       "192.168.1.111",
		RemotePort:       80,
		TunneledProtocol: "http",
	}

	// 快速启动一个隧道
	tunnelInstance, err := tunnel.FastStartTunnel(tunnelConfig)
	if err != nil {
		logger.Fatal("start tunnel failed, ", err.Error())
	}
	logger.Infof("local tunnel endpoint: %s", tunnelInstance.GetLocalEndpoint())

	// 注册退出信号
	ctx, done := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer func() {
		done()
		if r := recover(); r != nil {
			logger.Fatal("application panic, ", r)
		}
	}()

	// 阻塞，等待退出的信号
	for {
		select {
		case <-ctx.Done():
			tunnelInstance.Stop()
			logger.Infof("really stop tunnel!")
			return
		}
	}
}
