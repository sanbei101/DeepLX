package translate

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/phuslu/log"
)

// https://fanyi-api.baidu.com/product/113#
var BaiduErrorCodeMap = map[int]string{
	52000: "成功",
	52001: "请求超时,请检查传入的 q 参数是否是正常文本,以及 from 或 to 参数是否在支持的语种列表中",
	52002: "系统错误,请重试",
	52003: "未授权用户,请检查appid是否正确,或是否已开通对应服务",
	54000: "必填参数为空,请检查是否漏传、误传参数",
	54001: "签名错误,请检查签名生成方法是否有误",
	54003: "访问频率受限,请降低您的调用频率,或进行身份认证后切换为高级版/尊享版",
	54004: "账户余额不足,请前往管理控制台为账户充值",
	54005: "长query请求频繁,请降低长度大于1万字节query的发送频率,3s后再试",
	58000: "客户端IP非法,请检查开发者信息页面填写的对应服务器IP地址是否正确",
	58001: "译文语言方向不支持,请检查译文语言是否在语言列表里",
	58002: "服务当前已关闭,请前往管理控制台开启服务",
	58003: "此IP已被封禁,同一IP当日使用多个APPID发送翻译请求将被封禁,次日解封",
	90107: "认证未通过或未生效,请前往我的认证查看认证进度",
	20003: "请求内容存在安全风险,请检查请求文本是否涉及反动、暴力等相关内容",
}

func GetBaiduErrorMessage(errorCode int) string {
	if msg, ok := BaiduErrorCodeMap[errorCode]; ok {
		return fmt.Sprintf("百度翻译API错误 [%d]: %s", errorCode, msg)
	}
	return fmt.Sprintf("百度翻译API未知错误 [%d]", errorCode)
}

type BaiduTranslateRequest struct {
	Q     string `json:"q"`
	From  string `json:"from"`
	To    string `json:"to"`
	AppID string `json:"appid"`
	Salt  string `json:"salt"`
	Sign  string `json:"sign"`
}

type BaiduTranslateResponse struct {
	From        string `json:"from"`
	To          string `json:"to"`
	TransResult []struct {
		Src string `json:"src"`
		Dst string `json:"dst"`
	} `json:"trans_result"`
	ErrorCode *int `json:"error_code,omitempty"`
}

const BaiduTranslateURL = "https://fanyi-api.baidu.com/api/trans/vip/translate"

type BaiduConfig struct {
	AppID     string
	SecretKey string
}

type BaiduTranslator struct {
	appID     string
	secretKey string
	client    *http.Client
}

func NewBaiduTranslator(config BaiduConfig) *BaiduTranslator {
	return &BaiduTranslator{
		appID:     config.AppID,
		secretKey: config.SecretKey,
		client:    &http.Client{Timeout: 10 * time.Second},
	}
}

func (b *BaiduTranslator) Name() string {
	return "baidu"
}

func (b *BaiduTranslator) Translate(ctx context.Context, req *TranslateRequest) (*TranslateResponse, error) {
	q := strings.Join(req.TextList, "\n")

	salt := fmt.Sprintf("%d", time.Now().UnixNano())

	signStr := b.appID + q + salt + b.secretKey
	h := md5.New()
	h.Write([]byte(signStr))
	sign := hex.EncodeToString(h.Sum(nil))

	params := url.Values{}
	params.Set("q", q)
	params.Set("from", req.SourceLang)
	params.Set("to", req.TargetLang)
	params.Set("appid", b.appID)
	params.Set("salt", salt)
	params.Set("sign", sign)

	reqURL := BaiduTranslateURL + "?" + params.Encode()
	log.Info().Str("url", reqURL).Str("sign", sign).Msg("百度翻译请求")

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("创建百度翻译请求失败: %w", err)
	}

	resp, err := b.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("请求百度翻译API失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("百度翻译API返回非200状态码: %d", resp.StatusCode)
	}

	var result BaiduTranslateResponse

	if err := json.UnmarshalRead(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("解析百度翻译响应失败: %w", err)
	}

	if result.ErrorCode != nil && *result.ErrorCode != 52000 {
		return nil, fmt.Errorf("%s", GetBaiduErrorMessage(*result.ErrorCode))
	}

	translations := make([]Translation, 0, len(result.TransResult))
	for _, tr := range result.TransResult {
		translations = append(translations, Translation{
			DetectedSourceLang: result.From,
			Text:               tr.Dst,
		})
	}

	return &TranslateResponse{
		Translations: translations,
	}, nil
}
