package service

import (
	"flag"
	"fmt"
	"os"
)

type Config struct {
	IP    string
	Port  int
	Token string
	Proxy string
}

func InitConfig() *Config {
	cfg := &Config{
		IP:   "0.0.0.0",
		Port: 1188,
	}

	// IP 参数
	if ip, ok := os.LookupEnv("IP"); ok && ip != "" {
		cfg.IP = ip
	}
	flag.StringVar(&cfg.IP, "ip", cfg.IP, "绑定服务的 IP 地址")
	flag.StringVar(&cfg.IP, "i", cfg.IP, "绑定服务的 IP 地址")

	// 端口参数
	if port, ok := os.LookupEnv("PORT"); ok && port != "" {
		fmt.Sscanf(port, "%d", &cfg.Port)
	}
	flag.IntVar(&cfg.Port, "port", cfg.Port, "监听端口")
	flag.IntVar(&cfg.Port, "p", cfg.Port, "监听端口")

	// 访问令牌参数
	flag.StringVar(&cfg.Token, "token", "", "/translate 端点的访问令牌")
	if cfg.Token == "" {
		if token, ok := os.LookupEnv("TOKEN"); ok {
			cfg.Token = token
		}
	}

	// HTTP 代理参数
	flag.StringVar(&cfg.Proxy, "proxy", "", "HTTP 请求的代理 URL")
	if cfg.Proxy == "" {
		if proxy, ok := os.LookupEnv("PROXY"); ok {
			cfg.Proxy = proxy
		}
	}

	flag.Parse()
	return cfg
}
