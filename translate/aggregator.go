package translate

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"encoding/json/v2"

	validator "github.com/kamalyes/go-argus"
	"github.com/phuslu/log"
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

type TranslateRequest struct {
	SourceLang string   `json:"source_lang" validate:"required"`
	TargetLang string   `json:"target_lang" validate:"required"`
	TextList   []string `json:"text_list" validate:"required,min=1"`
}

type Translation struct {
	DetectedSourceLang string `json:"detected_source_lang,omitempty"`
	Text               string `json:"text"`
}

type TranslateResponse struct {
	Translations []Translation `json:"translations"`
}

type Translator interface {
	Name() string
	Translate(ctx context.Context, req *TranslateRequest) (*TranslateResponse, error)
}

type Aggregator struct {
	providers []Translator
}

func NewAggregator(providers ...Translator) *Aggregator {
	return &Aggregator{providers: providers}
}

func (a *Aggregator) Translate(ctx context.Context, req *TranslateRequest) (*TranslateResponse, error) {
	var lastErr error
	for _, provider := range a.providers {
		log.Info().Str("name", provider.Name()).Msg("尝试使用翻译引擎")

		resp, err := provider.Translate(ctx, req)
		if err == nil {
			log.Info().Str("name", provider.Name()).Msg("翻译成功")
			return resp, nil
		}

		log.Warn().Str("name", provider.Name()).Err(err).Msg("翻译失败")
		lastErr = err
	}
	return nil, fmt.Errorf("所有翻译引擎均失败, 最后一次错误: %w", lastErr)
}

func HandleTranslate(aggregator *Aggregator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 1024*1024)
		defer r.Body.Close()

		req, err := ReadBody[TranslateRequest](w, r)
		if err != nil {
			return
		}
		resp, err := aggregator.Translate(r.Context(), &req)
		if err != nil {
			Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		Success(w, resp)
	}
}
