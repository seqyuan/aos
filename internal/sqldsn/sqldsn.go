// Package sqldsn 构造 modernc.org/sqlite 使用的 file: DSN。
//
// modernc 的驱动在第一个 '?' 处拆分 DSN，路径部分原样传给 sqlite3_open_v2
// （带 SQLITE_OPEN_URI，SQLite 会做百分号解码）。因此路径中的 '?'、'#'、
// 空格等特殊字符必须先百分号编码，否则会被当作查询参数或 URI 特殊字符。
package sqldsn

import "strings"

// Pragma 追加的 SQLite 连接参数（WAL、busy_timeout、synchronous、foreign_keys）。
// db 与 manifest 共用，保证两处连接行为一致。
const Pragma = "_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)"

// File 返回给定数据库文件路径的完整 DSN。
func File(path string) string {
	return "file:" + escape(path) + "?" + Pragma
}

// escape 对路径做最小百分号编码：保留 '/' 与常规字符，编码 DSN/URI 特殊字符。
func escape(path string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(path); i++ {
		c := path[i]
		switch c {
		case '?', '#', '%', ' ', '\t', '\n', '\r', '\'', '"', '\\',
			'<', '>', '|', '{', '}', '^', '`':
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0x0F])
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
