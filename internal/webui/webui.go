// Package webui 以 //go:embed 内嵌前端构建产物（Vue3 产物）。
//
// ★ go build 不依赖前端构建：dist 目录内始终存在占位页 index.html（前端未构建时返回占位页）。
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// placeholderHTML 前端未构建时的占位页。
const placeholderHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head><meta charset="utf-8"><title>江熙新材审批系统</title></head>
<body style="font-family:-apple-system,Segoe UI,Roboto,sans-serif;padding:2rem;">
<h1>前端未构建</h1>
<p>尚未检测到前端构建产物。请运行 <code>scripts/build.sh</code>（含 <code>npm run build</code>）
生成 <code>web/dist</code> 并复制到 <code>internal/webui/dist</code> 后重新编译。</p>
</body>
</html>`

// Handler 返回静态资源处理器。
// dist 缺失或仅含占位页时，一律返回占位页，保证 go build/运行不依赖前端构建。
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return placeholderHandler()
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := strings.TrimPrefix(r.URL.Path, "/")
		if clean == "" {
			clean = "index.html"
		}
		if _, err := fs.Stat(sub, clean); err != nil {
			// SPA 回退：非静态资源路径返回 index.html
			if !strings.Contains(clean, ".") {
				if _, idxErr := fs.Stat(sub, "index.html"); idxErr == nil {
					serveIndex(w, sub)
					return
				}
			}
			// 未构建 → 占位页
			servePlaceholder(w)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

// Built 报告是否已内嵌真实前端产物（存在非占位的 index.html）。
func Built() bool {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return false
	}
	data, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		return false
	}
	return !strings.Contains(string(data), "前端未构建")
}

func placeholderHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { servePlaceholder(w) })
}

func servePlaceholder(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(placeholderHTML))
}

func serveIndex(w http.ResponseWriter, sub fs.FS) {
	data, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		servePlaceholder(w)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}
