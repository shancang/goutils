package goutils

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"
)

const (
	proxyPathKey                         = `proxyPath`
	proxyPathParamsKey                   = `*` + proxyPathKey
	roundRobin         LoadBalanceString = "round_robin"
)

type LoadBalanceString string

// ProxyConfig 代理配置
type ProxyConfig struct {
	BackendURL      []string          //目标地址
	TargetPath      string            //目标路径
	ReWritePath     string            //重写路径
	LoadBalanceMod  LoadBalanceString //算法
	healthCheckPath string            //健康检查路径
	healthInterval  int64             //健康检查间隔秒
	enableHealth    bool              //是否开启健康检查
}

type ConfigOptions func(*ProxyConfig)

func WithHealthCheckPath(path string) ConfigOptions {
	return func(config *ProxyConfig) {
		config.TargetPath = path
	}
}
func WithHealthInterval(interval int64) ConfigOptions {
	return func(config *ProxyConfig) {
		if interval <= 0 {
			return
		}
		config.healthInterval = interval
	}
}
func WithLoadBalanceMod(mod LoadBalanceString) ConfigOptions {
	return func(config *ProxyConfig) {
		if mod == "" {
			return
		}
		config.LoadBalanceMod = mod
	}
}
func WithReWritePath(path string) ConfigOptions {
	return func(config *ProxyConfig) {
		if path == "" {
			return
		}
		config.ReWritePath = path
	}
}
func WithHealthCheck(enable bool) ConfigOptions {
	return func(config *ProxyConfig) {
		config.enableHealth = enable
	}
}

func NewProxyConfig(BackendURL []string, TargetPath string, options ...ConfigOptions) *ProxyConfig {
	config := &ProxyConfig{
		BackendURL:      BackendURL,
		TargetPath:      TargetPath,
		healthCheckPath: "/health",  //默认健康检查路径
		healthInterval:  30,         //默认30秒
		LoadBalanceMod:  roundRobin, //默认轮询
	}
	for _, option := range options {
		option(config)
	}
	return config
}

type Backend struct {
	config      *ProxyConfig
	loadBalance AlgorithmInterface
	healthList  healthList
}

func (b *Backend) getBackendUrl() string {
	return b.loadBalance.Next()
}

func (b *Backend) ProxyHandler(c *gin.Context) {
	b.initLoadBalance()
	URL := b.getBackendUrl()
	if URL == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "backend url is empty", "msg": "proxy url parse error", "code": 1000, "data": nil})
		c.Abort()
		return
	}
	proxyUrl, err := url.Parse(URL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "msg": "proxy url parse error", "code": 1000, "data": nil})
		c.Abort()
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(proxyUrl)
	proxy.Director = func(req *http.Request) {
		req.Header.Set("X-Forwarded-Host", req.Header.Get("Host"))
		req.Header = c.Request.Header
		req.Host = proxyUrl.Host
		req.URL.Scheme = proxyUrl.Scheme
		req.URL.Host = proxyUrl.Host
		req.URL.Path = b.config.TargetPath + c.Param(proxyPathKey) //目标路径
	}
	proxy.ServeHTTP(c.Writer, c.Request)
}

func (b *Backend) initRouter(router *gin.RouterGroup, middleware ...gin.HandlerFunc) {
	group := b.config.ReWritePath + b.config.TargetPath
	r := router.Group(group)
	r.Use(middleware...)
	r.Any(proxyPathParamsKey, b.ProxyHandler)
}

func InitProxyRouter(configs []*ProxyConfig, router *gin.RouterGroup, middleware ...gin.HandlerFunc) {
	for _, config := range configs {
		b := Backend{
			config: config,
		}
		if b.config.enableHealth {
			go b.initHealthCheck()
		}
		b.initRouter(router, middleware...)
	}
}

func (b *Backend) initLoadBalance() {
	list := b.healthList.get()
	switch b.config.LoadBalanceMod {
	case roundRobin:
		b.loadBalance = NewRoundRobin(list)
	default:
		b.loadBalance = NewRoundRobin(list)
	}
}

func (b *Backend) healthCheck() {
	for _, u := range b.config.BackendURL {
		go func(u string) {
			active := true
			if _, err := http.Get(u); err != nil {
				active = false
			}
			b.healthList.add(u, active)
		}(u)
	}
}

func (b *Backend) initHealthCheck() {
	ticker := time.NewTicker(time.Duration(b.config.healthInterval) * time.Second)
	defer ticker.Stop()
	b.healthCheck()
	for {
		select {
		case <-ticker.C:
			b.healthCheck()
		}
	}
}

type healthList struct {
	url map[string]bool
	mu  sync.Mutex
}

func (h *healthList) add(key string, value bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.url == nil {
		h.url = make(map[string]bool)
	}
	h.url[key] = value
}

func (h *healthList) get() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var r []string
	for k, v := range h.url {
		if v {
			r = append(r, k)
		}
	}
	return r
}