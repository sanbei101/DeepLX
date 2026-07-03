package translate

// DeepLXTranslationResult 是 service 包中 HTTP 处理器使用的公共响应结构。
// 该结构早于向 oneshot 端点的迁移;Alternatives 现在始终为空,
// 因为 oneshot 不返回备选翻译,ID 由时间戳合成。
type DeepLXTranslationResult struct {
	Code         int      `json:"code"`
	ID           int64    `json:"id"`
	Message      string   `json:"message,omitempty"`
	Data         string   `json:"data"`
	Alternatives []string `json:"alternatives"`
	SourceLang   string   `json:"source_lang"`
	TargetLang   string   `json:"target_lang"`
	Method       string   `json:"method"`
}
