package common

import (
	"strconv"

	"github.com/gin-gonic/gin"
)

type PageInfo struct {
	Page     int `json:"page"`      // page num 页码
	PageSize int `json:"page_size"` // page size 页大小

	Total int `json:"total"` // 总条数，后设置
	Items any `json:"items"` // 数据，后设置

	// Substituted 表示请求显式给了一个不可用的分页值（负数页码或负的页大小），
	// 已由本 helper 换成可用值。它描述的是请求，不是这一页，所以不参与序列化。
	//
	// 之所以要暴露这个信号：下界以前是缺失的，负的 page_size 会原样变成
	// db.Limit(-1)，而 GORM 只在值 >= 0 时写 LIMIT 子句，于是"翻页"退化为全表返回。
	// 有些端点必须把这种请求判为客户端错误而不是悄悄分页（GET /api/models/search
	// 带 square_state 过滤时就是如此），它们过去靠比较 Page/PageSize 与 0 来发现，
	// 在下界补上之后那种写法恒为假，于是改用这个标志。
	Substituted bool `json:"-"`
}

func (p *PageInfo) GetStartIdx() int {
	return (p.Page - 1) * p.PageSize
}

func (p *PageInfo) GetEndIdx() int {
	return p.Page * p.PageSize
}

func (p *PageInfo) GetPageSize() int {
	return p.PageSize
}

func (p *PageInfo) GetPage() int {
	return p.Page
}

func (p *PageInfo) SetTotal(total int) {
	p.Total = total
}

func (p *PageInfo) SetItems(items any) {
	p.Items = items
}

func GetPageQuery(c *gin.Context) *PageInfo {
	pageInfo := &PageInfo{}
	// 手动获取并处理每个参数
	if page, err := strconv.Atoi(c.Query("p")); err == nil {
		pageInfo.Page = page
		pageInfo.Substituted = page < 0
	}
	if pageSize, err := strconv.Atoi(c.Query("page_size")); err == nil {
		pageInfo.PageSize = pageSize
		pageInfo.Substituted = pageInfo.Substituted || pageSize < 0
	}
	// 分页别名里出现显式负数与"没给"或"给了 0/不可解析的值"不是一回事：
	// 前者是客户端错误，后者只是取默认。夹住下界之后调用方没法再从结果里看出
	// 差别（结果永远合法），所以这里把它记下来，由调用方决定是否判错。
	for _, name := range []string{"ps", "size"} {
		if size, err := strconv.Atoi(c.Query(name)); err == nil && size < 0 {
			pageInfo.Substituted = true
		}
	}
	if pageInfo.Page < 1 {
		// 兼容：p 缺失、p=0、p 为负数或不可解析时都当作第一页。
		//
		// 负数必须在这里夹住而不是原样透传：GetStartIdx() 会算出负偏移，
		// 而负数 Offset 在 GORM 里根本不生成 OFFSET 子句，于是“翻页”退化成
		// 从头全量返回。
		pageInfo.Page = 1
	}

	// 下界与上界一样必须夹住：db.Limit(-1) 在 GORM 里不写 LIMIT 子句
	// （gorm.io/gorm/clause.Limit.Build 只在值 >= 0 时输出），单个查询参数
	// 就能把任何分页端点变成全表导出。0 与不可解析的值本来就落到别名/默认值，
	// 负数现在走同一条路：当作“没给”，而不是当作“不限”。
	if pageInfo.PageSize <= 0 {
		// 兼容
		pageSize, _ := strconv.Atoi(c.Query("ps"))
		if pageSize > 0 {
			pageInfo.PageSize = pageSize
		}
		if pageInfo.PageSize <= 0 {
			pageSize, _ = strconv.Atoi(c.Query("size")) // token page
			if pageSize > 0 {
				pageInfo.PageSize = pageSize
			}
		}
		if pageInfo.PageSize <= 0 {
			pageInfo.PageSize = ItemsPerPage
		}
	}

	if pageInfo.PageSize > 100 {
		pageInfo.PageSize = 100
	}

	return pageInfo
}
