package questions

// validCommand rejects unrelated fields rather than silently ignoring a wrong
// target or applying part of a malformed mutation.
func validCommand(c Command) bool {
	if c.Version < 0 || c.AnswerID < 0 || c.ReportID < 0 {
		return false
	}
	questionFields := c.Title != "" || len(c.Tags) != 0
	switch c.Action {
	case "create", "edit":
		return c.AnswerID == 0 && c.ReportID == 0 && !c.Enabled
	case "answer":
		return !questionFields && c.AnswerID == 0 && c.ReportID == 0 && !c.Enabled
	case "edit_answer":
		return !questionFields && c.AnswerID > 0 && c.ReportID == 0 && !c.Enabled
	case "note", "report":
		return !questionFields && c.ReportID == 0 && !c.Enabled
	case "accept":
		return !questionFields && c.Body == "" && c.ReportID == 0 && !c.Enabled
	case "hide", "lock":
		return !questionFields && c.Body == "" && c.AnswerID == 0 && c.ReportID == 0
	case "hide_answer":
		return !questionFields && c.Body == "" && c.AnswerID > 0 && c.ReportID == 0
	case "resolve_report":
		return !questionFields && c.Body == "" && c.AnswerID == 0 && c.ReportID > 0 && !c.Enabled
	default:
		return false
	}
}
