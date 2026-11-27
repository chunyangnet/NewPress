package resources

import "fmt"

func Label(c Condition) string {
	if c.Label != "" {
		return c.Label
	}
	switch c.Kind {
	case "public":
		return "所有人可访问"
	case "guest":
		return "游客专享"
	case "login":
		return "登录账号"
	case "membership":
		s := "有效会员"
		if len(c.Values) > 0 {
			s = "指定会员方案"
		}
		if c.Minimum > 0 {
			s += fmt.Sprintf(" · %d 级及以上", c.Minimum)
		}
		if c.Term == "monthly" {
			s += " · 月度"
		}
		if c.Term == "annual" {
			s += " · 年费"
		}
		return s
	case "level":
		return fmt.Sprintf("用户等级达到 Lv.%d", c.Minimum)
	case "group":
		return "属于指定用户组"
	case "follow":
		return "关注文章作者"
	case "like":
		return "点赞本篇文章"
	case "comment":
		return fmt.Sprintf("发表审核通过的评论（至少 %d 字）", max(1, c.Minimum))
	case "points":
		return fmt.Sprintf("使用 %d 积分", c.Amount)
	case "payment":
		if c.Currency == "JPY" {
			return fmt.Sprintf("支付 %d JPY", c.Amount)
		}
		return fmt.Sprintf("支付 %.2f %s", float64(c.Amount)/100, c.Currency)
	case "tip":
		return fmt.Sprintf("累计打赏达到 %d", c.Amount)
	case "quiz":
		return "答题通过"
	case "form":
		return "填写资料表单"
	}
	return "未满足的条件"
}
