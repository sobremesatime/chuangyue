package utils

// 分页默认值与上限，与 OpenAPI 契约保持一致。
const (
	DefaultPage     = 1
	DefaultPageSize = 20
	MaxPageSize     = 100
)

// NormalizePage 规范化分页参数：
//   - page 小于 1 时取默认值 1；
//   - pageSize 小于 1 时取默认值 20，超过上限时截断为 100。
//
// 返回的 offset 可直接用于 GORM 的 Offset()。
func NormalizePage(page, pageSize int) (int, int, int) {
	if page < 1 {
		page = DefaultPage
	}
	if pageSize < 1 {
		pageSize = DefaultPageSize
	}
	if pageSize > MaxPageSize {
		pageSize = MaxPageSize
	}
	return page, pageSize, (page - 1) * pageSize
}
