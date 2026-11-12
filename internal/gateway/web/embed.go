// Package web 以 go:embed 方式内嵌题目01 的可操作页面与静态资源。
// 页面随二进制发布，部署时无需额外挂载目录。
package web

import (
	"embed"
	"io/fs"
)

//go:embed webroot
var embedded embed.FS

// FS 返回去除 webroot 前缀后的静态资源文件系统（含 index.html 与 assets/）。
func FS() fs.FS {
	sub, err := fs.Sub(embedded, "webroot")
	if err != nil {
		panic("gateway web: embed sub fs failed: " + err.Error())
	}
	return sub
}
