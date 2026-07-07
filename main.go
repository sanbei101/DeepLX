package main

import (
	"encoding/json/v2"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	validator "github.com/kamalyes/go-argus"
	"github.com/phuslu/log"

	"github.com/sanbei101/translate-api/translate"
)

var validate = validator.New()

func init() {
	validator.SetLocale("zh")
}

type errorResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

func Success[T any](w http.ResponseWriter, data T) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if err := json.MarshalWrite(w, data); err != nil {
		log.Error().Err(err).Msg("Failed to write success response")
	}
}

func Error(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if err := json.MarshalWrite(w, errorResponse{Code: code, Msg: msg}); err != nil {
		log.Error().Err(err).Msg("Failed to write error response")
	}
}

func ReadBody[T any](w http.ResponseWriter, r *http.Request) (T, error) {
	var body T
	if err := json.UnmarshalRead(r.Body, &body); err != nil {
		log.Error().Err(err).Msg("Failed to read request body")
		Error(w, http.StatusBadRequest, "JSON 格式非法")
		return body, err
	}

	if err := validate.Struct(body); err != nil {
		errs := validator.TranslateValidationErrors(err, "zh")
		errorMsgs := make([]string, 0, len(errs))
		for i := range errs {
			errorMsgs = append(errorMsgs, errs[i].Field+": "+errs[i].Message)
		}
		fullErrorMsg := strings.Join(errorMsgs, "; ")
		Error(w, http.StatusBadRequest, fullErrorMsg)
		return body, err
	}

	return body, nil
}

func main() {
	log.Info().Msg("启动 DeepLX 服务...")

	// Initialize Baidu translator from environment variables.
	baiduConfig := translate.BaiduConfig{
		AppID:     os.Getenv("BAIDU_APPID"),
		SecretKey: os.Getenv("BAIDU_SECRET_KEY"),
	}

	if baiduConfig.AppID == "" || baiduConfig.SecretKey == "" {
		log.Fatal().Msg("请设置环境变量 BAIDU_APPID 和 BAIDU_SECRET_KEY")
	}

	baiduTranslator := translate.NewBaiduTranslator(baiduConfig)
	aggregator := translate.NewAggregator(baiduTranslator)

	// Set up routes.
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	r.Post("/translate", translate.HandleTranslate(aggregator))

	// Health check endpoint.
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	// Get port from environment or default to 8080.
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	server := &http.Server{
		Addr:         ":" + port,
		Handler:      r,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  30 * time.Second,
	}

	log.Info().Str("addr", server.Addr).Msg("服务启动")
	if err := server.ListenAndServe(); err != nil {
		log.Fatal().Err(err).Msg("服务启动失败")
	}
}
