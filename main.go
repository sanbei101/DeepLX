package main

import (
	"fmt"

	"github.com/gin-gonic/gin"

	"github.com/OwO-Network/DeepLX/service"
)

func main() {
	cfg := service.InitConfig()

	fmt.Printf("DeepL X has been successfully launched! Listening on %v:%v\n", cfg.IP, cfg.Port)
	fmt.Println("Developed by sjlleo <i@leo.moe> and missuo <me@missuo.me>.")

	// 设置应用为发布模式
	gin.SetMode(gin.ReleaseMode)

	app := service.Router(cfg)
	app.Run(fmt.Sprintf("%v:%v", cfg.IP, cfg.Port))
}
