package i18n

// messagesZh is the Simplified-Chinese catalogue. The key set must
// match messagesEn exactly (TestCatalogueParity). Translations keep
// the same positional placeholders (%s / %d / %q) as the English
// source; reordering arguments requires touching every catalogue in
// the same change.
//
// Punctuation note: full-width （）：、。—— are fine here — these
// strings render in the chat / popup zone, not the status bar, so the
// width-oracle discipline that keeps statusbar.go ASCII does not
// apply. The only constraint is that these strings never enter the
// LLM prompt path (see i18n.go's scope note).
var messagesZh = map[string]string{
	"view.starting":        "启动中…",
	"view.provider_banner": "⚠ 提供方：%s — FIM / 缓存统计 / Reasoner 已禁用",
	"view.thinking":        "思考中…",
	"view.thinking_s":      "思考中… %d 秒",
	"view.thinking_ms":     "思考中… %d 分 %d 秒",
	"view.queue.steering":  "↪ 转向中：",
	"view.queue.queued":    "↰ 已排队：",

	"menu.hint.simple": "  ↑/↓ 移动 · Enter 采纳 · Esc 取消",
	"menu.hint.toggle": "  ↑/↓ 移动 · Space 勾选 · Enter 确认 · Esc 取消",

	"cmd.unknown": "未知命令 %s — 试试 /help",

	"lang.status":         "语言：%s（界面文案）。用 /lang <en|zh> 切换；SEEK_LANG 可按次覆盖。",
	"lang.switched":       "已切换到 %s。已渲染的旧行保持原语言。",
	"lang.unsupported":    "不支持的语言 %q —— 可选：en、zh。",
	"lang.persist_failed": "警告：语言设置未能写入 ~/.seek/config.json：%v",
}
