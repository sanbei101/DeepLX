package translate

import (
	"compress/flate"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/andybalholm/brotli"
	"github.com/imroc/req/v3"
	"github.com/tidwall/gjson"
)

// DeepL 的交互式网页翻译器已迁移到 SignalR/WebSocket 频道,
// 而 www2.deepl.com 上的传统 LMT_handle_texts 后端现在会对匿名请求快速返回 429。
// 官方 Chrome 扩展则向一个无状态的 "oneshot" 端点发送 POST 请求,
// 该端点位于独立的速率限制池中,并对匿名请求接受字面量头
// `Authorization: None`--这就是我们的目标。
//
// 我们发送的请求是从扩展的 background.js 逆向工程而来
// (Chrome Web Store ID cofdbpoegempjloogbagkncekinflcnj):
//   - URL 构建器   → mN(),偏移量约 529948
//   - 请求体构建器  → IN(),偏移量约 531200
//   - fetch 封装器  → JO(),偏移量约 508659
//   - 应用元数据    → Wo(),偏移量约 16500
const (
	oneshotFreeEndpoint = "https://oneshot-free.www.deepl.com/v1/translate"

	impersonatedChromeMajor = "120"
	chromeExtensionVersion  = "1.86.0"
	chromeExtensionID       = "cofdbpoegempjloogbagkncekinflcnj"

	maxFreeTextLength = 1500
	oneshotTimeout    = 20 * time.Second
	warmupTimeout     = 5 * time.Second
)

// instanceID 镜像扩展在安装时持久化到 chrome.storage 的 UUID:
// 在进程生命周期内保持稳定,在每个请求中重复使用。
// 每次请求都轮换它会是一个远为强烈的信号,远甚于复用同一个。
var instanceID = newInstanceID()

// 真实的扩展 fetch() 会继承浏览器在 .deepl.com 上积累的所有 cookie。
// 首次访问 www.deepl.com 会设置 userCountry=<iso2> 和 verifiedBot=false;
// 曾经打开过该网站的用户还额外有来自分析 JS 的 _ga / _ga_<id>。
// 我们共享一个进程级的 cookie jar,因此每个 oneshot POST 都会自动携带
// 预热 GET 请求获取到的 cookie。
var (
	cookieJar     http.CookieJar
	cookieJarOnce sync.Once
	cookieWarmer  sync.Once
)

// oneshotClients 为每个代理 URL 缓存一个 req.Client,
// 使所有翻译调用共享底层的 TCP / TLS / HTTP/2 连接池。
// 每次请求都创建新的 req.Client 意味着每次都进行全新的 TLS 握手
// (在 DeepL 自身约 1.5 秒的处理延迟之外额外增加 200-400 毫秒的开销)。
// 复用客户端可以让 keep-alive 和会话票据在热路径上将开销降至接近零。
var oneshotClients sync.Map // map[string]*req.Client

func sharedCookieJar() http.CookieJar {
	cookieJarOnce.Do(func() {
		j, _ := cookiejar.New(nil)
		cookieJar = j
	})
	return cookieJar
}

// warmCookies 通过向 www.deepl.com 发送一次 GET 请求来预热共享 jar。
// Set-Cookie 响应(userCountry / verifiedBot)落在 .deepl.com 上,
// 这是 oneshot-free.www.deepl.com 的 eTLD+1,
// 因此后续对 oneshot 端点的 POST 会自动携带这些 cookie。
// 同一个请求同时兼作 TLS 握手预热:它在客户端池中留下一个到
// www.deepl.com 的活跃 HTTP/2 连接,第一个 oneshot POST 随后通过
// TLS 会话票据恢复该连接。
func warmCookies(client *req.Client) {
	cookieWarmer.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), warmupTimeout)
		defer cancel()
		_, _ = client.R().SetContext(ctx).Get("https://www.deepl.com/translator")
	})
}

func newInstanceID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40 // RFC 4122 v4
	b[8] = (b[8] & 0x3f) | 0x80
	s := hex.EncodeToString(b)
	return fmt.Sprintf("%s-%s-%s-%s-%s", s[0:8], s[8:12], s[12:16], s[16:20], s[20:32])
}

// 语言代码表镜像扩展 background.js 中的内置列表
// (数组 `y`,偏移量约 6000,包含全部支持作为目标的语言;
// 数组 `A` 为仅作为源语言的别名)。
// 键是调用者传入的大写形式;值是 oneshot 端点期望的小写 BCP-47 -ish 形式
// (如 "de"、"en-US"、"zh-Hans" ...)。
//
// targetLangMap 是 API 接受的 `target_lang`。EN 和 PT 故意被排除--
// DeepL 已废弃它们作为目标代码,转而使用 EN-US/EN-GB 和 PT-BR/PT-PT,
// 扩展的 y 数组也反映了这一点。我们接受 EN/PT 作为向后兼容的便利,
// 并将它们解析为区域默认值(en-US、pt-BR)。
var targetLangMap = map[string]string{
	"AR": "ar", "BG": "bg", "CS": "cs", "DA": "da", "DE": "de", "EL": "el",
	"EN-GB": "en-GB", "EN-US": "en-US",
	"ES": "es", "ES-419": "es-419", "ET": "et", "FI": "fi", "FR": "fr",
	"HE": "he", "HU": "hu", "ID": "id", "IT": "it", "JA": "ja", "KO": "ko",
	"LT": "lt", "LV": "lv", "NB": "nb", "NL": "nl", "PL": "pl",
	"PT-BR": "pt-BR", "PT-PT": "pt-PT",
	"RO": "ro", "RU": "ru", "SK": "sk", "SL": "sl", "SV": "sv",
	"TR": "tr", "UK": "uk", "VI": "vi",
	"ZH": "zh-Hans", "ZH-HANS": "zh-Hans", "ZH-HANT": "zh-Hant",
	// 为旧调用者提供的便利别名
	"EN": "en-US",
	"PT": "pt-BR",
}

// sourceLangMap 是 API 接受的 `source_lang`。它是 targetLangMap 的超集:
// EN 和 PT 是一级源语言代码(扩展数组 `A`),映射为通用的 "en"/"pt"--
// 用于调用者知道输入是英语/葡萄牙语但不想指定区域变体的情况。
var sourceLangMap = func() map[string]string {
	m := make(map[string]string, len(targetLangMap)+2)
	for k, v := range targetLangMap {
		m[k] = v
	}
	m["EN"] = "en"
	m["PT"] = "pt"
	return m
}()

// resolveTargetLang 验证并规范化用户提供的目标语言代码。
// 如果代码为空、"auto" 或不在支持的集合中,返回 "" 和非 nil 错误。
func resolveTargetLang(code string) (string, error) {
	if code == "" {
		return "", fmt.Errorf("target_lang 为必填项")
	}
	if strings.EqualFold(code, "auto") {
		return "", fmt.Errorf("target_lang 不能为 \"auto\";请选择以下之一: %s", supportedTargetLangsList())
	}
	if v, ok := targetLangMap[strings.ToUpper(code)]; ok {
		return v, nil
	}
	return "", fmt.Errorf("不支持的 target_lang %q;有效代码: %s", code, supportedTargetLangsList())
}

// resolveSourceLang 验证并规范化用户提供的源语言代码。
// 空字符串或 "auto" 是允许的,返回 ("", nil),
// 使调用者省略 source_lang 并让服务器自动检测。
func resolveSourceLang(code string) (string, error) {
	if code == "" || strings.EqualFold(code, "auto") {
		return "", nil
	}
	if v, ok := sourceLangMap[strings.ToUpper(code)]; ok {
		return v, nil
	}
	return "", fmt.Errorf("不支持的 source_lang %q;有效代码: %s(或 \"auto\")", code, supportedSourceLangsList())
}

// supportedTargetLangsList / supportedSourceLangsList 返回排序后的、
// 逗号分隔的支持代码列表,用于错误信息。在首次调用时缓存。
var (
	targetLangsListOnce sync.Once
	targetLangsList     string
	sourceLangsListOnce sync.Once
	sourceLangsList     string
)

func supportedTargetLangsList() string {
	targetLangsListOnce.Do(func() {
		targetLangsList = sortedKeys(targetLangMap)
	})
	return targetLangsList
}

func supportedSourceLangsList() string {
	sourceLangsListOnce.Do(func() {
		sourceLangsList = sortedKeys(sourceLangMap)
	})
	return sourceLangsList
}

func sortedKeys(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

// appInformation 匹配 background.js 中 Wo({isSnakeCase: true}) 生成的 snake_case 格式。
// 值与 TLS 握手中固定的 Chrome 版本保持一致,使请求在各方面讲述同一个故事。
type appInformation struct {
	OS         string `json:"os"`
	OSVersion  string `json:"os_version"`
	AppVersion string `json:"app_version"`
	AppBuild   string `json:"app_build"`
	InstanceID string `json:"instance_id"`
}

// oneshotRequest 镜像 background.js 中 IN(...) 构建的请求体。
// 字段顺序与扩展的对象字面量保持一致,使序列化后的 JSON 字节完全相同
// (encoding/json 遵循结构体字段顺序)。
type oneshotRequest struct {
	Text           []string       `json:"text"`
	TargetLang     string         `json:"target_lang"`
	SourceLang     string         `json:"source_lang,omitempty"`
	UsageType      string         `json:"usage_type"`
	AppInformation appInformation `json:"app_information"`
}

// newOneshotClient 配置一个 req.Client,其出站配置在可能的情况下
// 与 chrome 扩展 service-worker fetch() 逐字节匹配。
// ImpersonateChrome 为我们提供 Chrome 120 的 TLS ClientHello、HTTP/2 SETTINGS、
// pseudo/头顺序,以及与之绑定的 sec-ch-ua/user-agent 集合。
// 它还安装了一组具有导航风格的常见头(pragma、cache-control、
// upgrade-insecure-requests、sec-fetch-user),这些是 fetch() 永远不会发出的--
// 清除它们,使 WAF 无法在此维度上区分我们。
//
// getOneshotClient 返回针对给定代理 URL 的进程级缓存客户端,首次使用时创建。
// 在请求之间共享客户端是热路径上最大的延迟优化:
// 它保持 TLS / HTTP/2 连接在池中,使后续请求完全跳过握手。
// 在首次创建时在后台启动 cookie jar 预热,
// 使第一个真正的翻译调用在 TLS 握手进行中就能并行运行。
func getOneshotClient(proxyURL string) (*req.Client, error) {
	if c, ok := oneshotClients.Load(proxyURL); ok {
		return c.(*req.Client), nil
	}
	c, err := newOneshotClient(proxyURL)
	if err != nil {
		return nil, err
	}
	if actual, loaded := oneshotClients.LoadOrStore(proxyURL, c); loaded {
		return actual.(*req.Client), nil
	}
	// 第一次看到这个代理。在后台启动预热,
	// 使第一个翻译调用可以与到 www.deepl.com 的 TLS 握手并行运行。
	go warmCookies(c)
	return c, nil
}

func newOneshotClient(proxyURL string) (*req.Client, error) {
	client := req.C().ImpersonateChrome().SetCookieJar(sharedCookieJar()).SetTimeout(oneshotTimeout)
	for _, h := range []string{
		"Pragma",
		"Cache-Control",
		"Upgrade-Insecure-Requests",
		"Sec-Fetch-User",
	} {
		client.Headers.Del(h)
	}
	// Chrome 120 的 fetch() 声明支持 gzip/deflate/br
	// (zstd 直到 Chrome 123+ 才成为默认值)。
	// req 的默认值仅为 "gzip",这是一个可区分的信号--明确匹配 Chrome。
	client.SetCommonHeader("Accept-Encoding", "gzip, deflate, br")

	if proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err != nil {
			return nil, err
		}
		client.SetProxyURL(u.String())
	}
	return client, nil
}

// callOneshot 向 oneshot 端点发送 POST 请求并返回解析后的 JSON。
// 对于匿名流量,bearerToken 为空,我们发送字面量头
// `Authorization: None`--精确复制扩展的 JO() 封装器。
// 如果省略该头,请求将被置于不同的服务端认证分支。
func callOneshot(endpoint string, body []byte, proxyURL string) (gjson.Result, int, error) {
	client, err := getOneshotClient(proxyURL)
	if err != nil {
		return gjson.Result{}, 0, err
	}

	resp, err := client.R().
		DisableAutoReadResponse().
		SetHeader("Content-Type", "application/json").
		SetHeader("Accept", "*/*").
		SetHeader("Authorization", "None").
		SetHeader("Origin", "chrome-extension://"+chromeExtensionID).
		SetHeader("Sec-Fetch-Site", "cross-site").
		SetHeader("Sec-Fetch-Mode", "cors").
		SetHeader("Sec-Fetch-Dest", "empty").
		SetBodyBytes(body).
		Post(endpoint)
	if err != nil {
		return gjson.Result{}, 0, err
	}
	defer resp.Body.Close()

	// 一旦我们手动设置了 Accept-Encoding,Go 的 HTTP 栈就会停止
	// 透明解压,因此手动处理 gzip/deflate/br。
	var reader io.Reader = resp.Body
	switch strings.ToLower(resp.Header.Get("Content-Encoding")) {
	case "gzip":
		gr, err := gzip.NewReader(resp.Body)
		if err != nil {
			return gjson.Result{}, resp.StatusCode, fmt.Errorf("gzip reader: %w", err)
		}
		defer gr.Close()
		reader = gr
	case "deflate":
		reader = flate.NewReader(resp.Body)
	case "br":
		reader = brotli.NewReader(resp.Body)
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		return gjson.Result{}, resp.StatusCode, fmt.Errorf("读取响应体: %w", err)
	}
	return gjson.ParseBytes(raw), resp.StatusCode, nil
}

// TranslateByDeepLX 通过 DeepL oneshot 端点执行翻译。
func TranslateByDeepLX(sourceLang, targetLang, text string, tagHandling string, proxyURL string) (DeepLXTranslationResult, error) {
	if text == "" {
		return DeepLXTranslationResult{
			Code:    http.StatusNotFound,
			Message: "No text to translate",
		}, nil
	}

	resolvedTarget, err := resolveTargetLang(targetLang)
	if err != nil {
		return DeepLXTranslationResult{
			Code:    http.StatusBadRequest,
			Message: err.Error(),
		}, nil
	}
	resolvedSource, err := resolveSourceLang(sourceLang)
	if err != nil {
		return DeepLXTranslationResult{
			Code:    http.StatusBadRequest,
			Message: err.Error(),
		}, nil
	}

	if n := utf8.RuneCountInString(text); n > maxFreeTextLength {
		return DeepLXTranslationResult{
			Code:    http.StatusRequestEntityTooLarge,
			Message: fmt.Sprintf("text exceeds maximum length: %d characters (anonymous oneshot limit is %d)", n, maxFreeTextLength),
		}, nil
	}

	reqStruct := oneshotRequest{
		Text:       []string{text},
		TargetLang: resolvedTarget,
		SourceLang: resolvedSource, // 空值 = 自动检测;omitempty 会省略该字段
		UsageType:  "Translate",
		AppInformation: appInformation{
			OS:         "brex_macOS",
			OSVersion:  "brex_chrome_" + impersonatedChromeMajor + ".0.0.0",
			AppVersion: chromeExtensionVersion,
			AppBuild:   "chrome_web_store",
			InstanceID: instanceID,
		},
	}
	bodyBytes, _ := json.Marshal(reqStruct)

	id := time.Now().UnixMilli()
	result, status, err := callOneshot(oneshotFreeEndpoint, bodyBytes, proxyURL)
	if err != nil {
		// 将上游超时映射为 504,使调用者可以区分 "DeepL 耗时过长"
		// 与其他 503 故障模式(DNS、TLS 等)。
		var ue *url.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ue) && ue.Timeout()) {
			return DeepLXTranslationResult{
				ID:      id,
				Code:    http.StatusGatewayTimeout,
				Message: fmt.Sprintf("上游 DeepL 请求在 %s 后超时", oneshotTimeout),
			}, nil
		}
		return DeepLXTranslationResult{
			ID:      id,
			Code:    http.StatusServiceUnavailable,
			Message: err.Error(),
		}, nil
	}

	switch status {
	case http.StatusOK:
		// 继续执行响应体解析
	case http.StatusTooManyRequests:
		return DeepLXTranslationResult{
			ID:      id,
			Code:    http.StatusTooManyRequests,
			Message: "请求过于频繁,您的 IP 已被 DeepL 临时封禁,请勿在短时间内频繁请求",
		}, nil
	default:
		return DeepLXTranslationResult{
			ID:      id,
			Code:    http.StatusServiceUnavailable,
			Message: fmt.Sprintf("请求失败,状态码: %d", status),
		}, nil
	}

	translations := result.Get("translations").Array()
	if len(translations) == 0 {
		return DeepLXTranslationResult{
			ID:      id,
			Code:    http.StatusServiceUnavailable,
			Message: "翻译失败",
		}, nil
	}

	mainText := translations[0].Get("text").String()
	if mainText == "" {
		return DeepLXTranslationResult{
			ID:      id,
			Code:    http.StatusServiceUnavailable,
			Message: "翻译失败",
		}, nil
	}

	if detected := translations[0].Get("detected_source_language").String(); detected != "" {
		sourceLang = strings.ToUpper(detected)
	}

	return DeepLXTranslationResult{
		Code:         http.StatusOK,
		ID:           id,
		Data:         mainText,
		Alternatives: nil, // oneshot 不返回备选翻译
		SourceLang:   sourceLang,
		TargetLang:   targetLang,
		Method:       "Free",
	}, nil
}
