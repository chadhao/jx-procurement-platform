package dashboard

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"sort"
	"strings"
)

// Flatten 将看板结果摊平为「指标行」列表（每行一个键值对映射），供导出复用。
//
// ★ 列级投影复用：调用方（接入层）对返回的每一行再次调用 permission.Project，
// 与 JSON 响应使用同一投影器，禁止绕过列过滤（架构 §5.3 / TC-07 步骤 4）。
func Flatten(res Result) []map[string]any {
	out := make([]map[string]any, 0, len(res.Cards)+len(res.Alerts)+8)

	for _, c := range res.Cards {
		row := map[string]any{"section": "card", "dashboard": res.Name, "period": res.Period}
		mergeRow(row, c)
		out = append(out, row)
	}
	for _, ch := range res.Charts {
		for _, p := range ch.Series {
			row := map[string]any{"section": "chart", "dashboard": res.Name, "period": res.Period,
				"chart_key": ch.Key, "chart_type": ch.Type}
			mergeRow(row, p)
			out = append(out, row)
		}
	}
	for _, a := range res.Alerts {
		alertKey, _ := a["key"].(string)
		// ★ N-078：清单型告警（「1,000 元以下高频供应商」/「拆分嫌疑」）的组明细
		//   **逐条展开为独立导出行** —— 使清单可**逐笔回溯**（供应商 / 金额 / 日期 / 单号），
		//   而不是把整份清单挤进一个单元格。★ 无 detail 的告警行为不变。
		for i, d := range detailRows(a["detail"]) {
			dr := map[string]any{
				"section": "alert_detail", "dashboard": res.Name, "period": res.Period,
				"alert_key": alertKey, "detail_index": i + 1,
			}
			mergeRow(dr, d)
			out = append(out, dr)
		}
		row := map[string]any{"section": "alert", "dashboard": res.Name, "period": res.Period}
		for k, v := range a {
			if k == "detail" {
				continue // detail 已展开为独立行（section=alert_detail），摘要行不再重复整份清单
			}
			row[k] = v
		}
		out = append(out, row)
	}
	if res.Supervision != nil {
		if v, ok := res.Supervision["requester_as_handler_count"]; ok {
			out = append(out, map[string]any{
				"section": "supervision", "dashboard": res.Name, "period": res.Period,
				"key": "requester_as_handler_count", "label": "需求提出人任经办人的笔数",
				"value": v,
			})
		}
		if list, ok := res.Supervision["handler_concentration"].([]map[string]any); ok {
			for _, c := range list {
				row := map[string]any{
					"section": "supervision", "dashboard": res.Name, "period": res.Period,
					"key": "handler_concentration",
				}
				mergeRow(row, c)
				out = append(out, row)
			}
		}
	}
	return out
}

// UnionKeys 返回一组行映射的键并集（升序），用作导出表头。
func UnionKeys(rows []map[string]any) []string {
	set := map[string]bool{}
	for _, r := range rows {
		for k := range r {
			set[k] = true
		}
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Stringify 按表头顺序将行映射转为字符串矩阵（缺失列置空）。
func Stringify(headers []string, rows []map[string]any) [][]string {
	out := make([][]string, 0, len(rows))
	for _, r := range rows {
		line := make([]string, len(headers))
		for i, h := range headers {
			line[i] = cellString(r[h])
		}
		out = append(out, line)
	}
	return out
}

func mergeRow(dst, src map[string]any) {
	for k, v := range src {
		dst[k] = v
	}
}

// detailRows 归一「组明细」为 []map[string]any。
//
// ★ 兼容两种形态：未经列投影的 `[]map[string]any`（聚合直造）与经 `permission.ProjectDeep`
// 投影后的 `[]any`（其 `[]map[string]any` 分支返回的是 `[]any` —— 见 permission/projectNested）。
func detailRows(v any) []map[string]any {
	switch t := v.(type) {
	case []map[string]any:
		return t
	case []any:
		out := make([]map[string]any, 0, len(t))
		for _, e := range t {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	default:
		return nil
	}
}

func cellString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		return fmt.Sprintf("%g", t)
	case int:
		return fmt.Sprintf("%d", t)
	case int64:
		return fmt.Sprintf("%d", t)
	default:
		return fmt.Sprintf("%v", t)
	}
}

// FormatCSV 生成 CSV 字节流（含表头；字段按需加引号；CRLF 行结束）。
func FormatCSV(headers []string, rows [][]string) []byte {
	var b bytes.Buffer
	b.WriteString(csvLine(headers))
	for _, r := range rows {
		b.WriteString(csvLine(r))
	}
	// UTF-8 BOM：便于 Excel 正确识别中文。
	return append([]byte{0xEF, 0xBB, 0xBF}, b.Bytes()...)
}

func csvLine(fields []string) string {
	parts := make([]string, len(fields))
	for i, f := range fields {
		parts[i] = csvField(f)
	}
	return strings.Join(parts, ",") + "\r\n"
}

func csvField(s string) string {
	if strings.ContainsAny(s, ",\"\r\n") {
		return "\"" + strings.ReplaceAll(s, "\"", "\"\"") + "\""
	}
	return s
}

// FormatXLSX 生成最小可用的 XLSX（纯标准库：archive/zip + 内联字符串）。
// 不引入任何第三方依赖（硬约束）；Excel / WPS 均可打开。
func FormatXLSX(headers []string, rows [][]string) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	parts := []struct {
		name    string
		content string
	}{
		{"[Content_Types].xml", contentTypesXML},
		{"_rels/.rels", relsXML},
		{"xl/workbook.xml", workbookXML},
		{"xl/_rels/workbook.xml.rels", workbookRelsXML},
		{"xl/worksheets/sheet1.xml", sheetXML(headers, rows)},
	}
	for _, p := range parts {
		w, err := zw.Create(p.name)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write([]byte(p.content)); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func sheetXML(headers []string, rows [][]string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	b.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	writeRow := func(rowIdx int, cells []string) {
		b.WriteString(fmt.Sprintf(`<row r="%d">`, rowIdx))
		for ci, v := range cells {
			ref := colLetter(ci) + fmt.Sprintf("%d", rowIdx)
			b.WriteString(`<c r="` + ref + `" t="inlineStr"><is><t xml:space="preserve">`)
			b.WriteString(xmlEscape(v))
			b.WriteString(`</t></is></c>`)
		}
		b.WriteString(`</row>`)
	}
	writeRow(1, headers)
	for i, r := range rows {
		writeRow(i+2, r)
	}
	b.WriteString(`</sheetData></worksheet>`)
	return b.String()
}

func colLetter(idx int) string {
	name := ""
	for idx >= 0 {
		name = string(rune('A'+idx%26)) + name
		idx = idx/26 - 1
	}
	return name
}

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

const contentTypesXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>`

const relsXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`

const workbookXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="dashboard" sheetId="1" r:id="rId1"/></sheets></workbook>`

const workbookRelsXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`
